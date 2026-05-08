package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"tabelog-map/models"
)

func CreateTables(db *sql.DB) error {
	_, err := db.Exec(`CREATE EXTENSION IF NOT EXISTS postgis`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS restaurants (
			id BIGSERIAL PRIMARY KEY,
			name TEXT,
			alias TEXT,
			pillow TEXT,
			address TEXT,
			url TEXT UNIQUE,
			rating DOUBLE PRECISION,
			latitude DOUBLE PRECISION,
			longitude DOUBLE PRECISION,
			lunch_min_price INTEGER,
			lunch_max_price INTEGER,
			dinner_min_price INTEGER,
			dinner_max_price INTEGER,
			nearest_station TEXT,
			city TEXT,
			kids TEXT
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`ALTER TABLE restaurants ADD COLUMN IF NOT EXISTS photos TEXT`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`ALTER TABLE restaurants ADD COLUMN IF NOT EXISTS location geometry(Point, 4326)`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		UPDATE restaurants
		SET location = ST_SetSRID(ST_MakePoint(longitude, latitude), 4326)
		WHERE location IS NULL AND longitude != 0 AND latitude != 0
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_restaurants_location ON restaurants USING GIST (location)`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS categories (
			id BIGSERIAL PRIMARY KEY,
			name TEXT UNIQUE
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS restaurant_categories (
			restaurant_id BIGINT REFERENCES restaurants(id) ON DELETE CASCADE,
			category_id  BIGINT REFERENCES categories(id) ON DELETE CASCADE,
			UNIQUE (restaurant_id, category_id)
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS scrape_progress (
			url        TEXT PRIMARY KEY,
			status     TEXT NOT NULL DEFAULT 'pending',
			error_msg  TEXT,
			attempts   INT  NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	return err
}

// SeedProgress bulk-inserts URLs into scrape_progress as 'pending'.
// Existing rows are left unchanged (ON CONFLICT DO NOTHING).
func SeedProgress(db *sql.DB, urls []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO scrape_progress (url) VALUES ($1) ON CONFLICT (url) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, u := range urls {
		if _, err := stmt.Exec(u); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResetErrors resets all error-status rows back to pending for retry.
func ResetErrors(db *sql.DB) error {
	_, err := db.Exec(`UPDATE scrape_progress SET status = 'pending', error_msg = NULL WHERE status = 'error'`)
	return err
}

// LoadPendingURLs returns all URLs with status 'pending'.
func LoadPendingURLs(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT url FROM scrape_progress WHERE status = 'pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, rows.Err()
}

// MarkProgress updates the status and optional error message for a URL.
func MarkProgress(db *sql.DB, url, status, errMsg string) {
	var msgArg interface{}
	if errMsg != "" {
		msgArg = errMsg
	}
	_, err := db.Exec(
		`UPDATE scrape_progress SET status=$1, error_msg=$2, attempts=attempts+1, updated_at=NOW() WHERE url=$3`,
		status, msgArg, url,
	)
	if err != nil {
		fmt.Printf("warning: could not update progress for %s: %v\n", url, err)
	}
}

func marshalPhotos(photos []string) *string {
	if len(photos) == 0 {
		return nil
	}
	b, _ := json.Marshal(photos)
	s := string(b)
	return &s
}

func InsertRestaurant(db *sql.DB, r models.Restaurant) (int64, error) {
	var restaurantID int64

	err := db.QueryRow(`
		INSERT INTO restaurants (
			name, address, alias, pillow, url, rating, latitude, longitude, location,
			lunch_min_price, lunch_max_price,
			dinner_min_price, dinner_max_price,
			nearest_station, city, kids, photos
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, ST_SetSRID(ST_MakePoint($8, $7), 4326),
			$9, $10, $11, $12, $13, $14, $15, $16
		)
		ON CONFLICT (url)
		DO UPDATE SET
			name = EXCLUDED.name,
			address = EXCLUDED.address,
			kids = EXCLUDED.kids,
			photos = EXCLUDED.photos,
			location = EXCLUDED.location
		RETURNING id;
	`,
		r.Name,
		r.Address,
		r.Alias,
		r.PillowWord,
		r.URL,
		r.Rating,
		r.Latitude,
		r.Longitude,
		r.LunchMinPrice,
		r.LunchMaxPrice,
		r.DinnerMinPrice,
		r.DinnerMaxPrice,
		r.NearestStation,
		r.City,
		r.Kids,
		marshalPhotos(r.Photos),
	).Scan(&restaurantID)

	if err != nil {
		return 0, err
	}

	for _, c := range r.Categories {
		c = strings.TrimSpace(c)

		var categoryID int64

		// insert category if not exists, return id either way
		err = db.QueryRow(`
			INSERT INTO categories (name)
			VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id
		`, c).Scan(&categoryID)

		if err != nil {
			return restaurantID, err
		}

		// link restaurant <-> category
		_, err = db.Exec(`
			INSERT INTO restaurant_categories (restaurant_id, category_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, restaurantID, categoryID)

		if err != nil {
			return restaurantID, err
		}
	}

	return restaurantID, nil
}
