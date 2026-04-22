package db

import (
	"database/sql"
	"strings"

	"tabelog-map/models"
)

func CreateTables(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS restaurants (
			id BIGSERIAL PRIMARY KEY,
			name TEXT,
			alias TEXT,
			pillow TEXT,
			address TEXT,
			url TEXT,
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
	return err
}

func InsertRestaurant(db *sql.DB, r models.Restaurant) (int64, error) {
	var restaurantID int64

	err := db.QueryRow(`
		INSERT INTO restaurants (
			name, address, alias, pillow, url, rating, latitude, longitude,
			lunch_min_price, lunch_max_price,
			dinner_min_price, dinner_max_price,
			nearest_station, city, kids
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15
		)
		ON CONFLICT (url)
		DO UPDATE SET
			name = EXCLUDED.name,
			address = EXCLUDED.address,
			kids = EXCLUDED.kids
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
