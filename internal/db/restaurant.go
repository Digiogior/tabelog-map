package db

import (
	"database/sql"
	"fmt"
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
	return CreateMenuTables(db)
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

		err = db.QueryRow(`
			INSERT INTO categories (name)
			VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id
		`, c).Scan(&categoryID)

		if err != nil {
			return restaurantID, err
		}

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

func GetCategories(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM categories ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		categories = append(categories, name)
	}
	return categories, rows.Err()
}

func GetNearbyRestaurants(db *sql.DB, lat, lng float64, radiusMeters int, category string) ([]models.Restaurant, error) {
	query := `
		SELECT r.name, r.address, r.url, r.rating, r.latitude, r.longitude,
		       r.lunch_min_price, r.lunch_max_price,
		       r.dinner_min_price, r.dinner_max_price,
		       r.nearest_station, r.city, r.kids, r.id
		FROM restaurants r
	`
	args := []interface{}{lng, lat, radiusMeters}

	if category != "" {
		query += `
		JOIN restaurant_categories rc ON rc.restaurant_id = r.id
		JOIN categories c ON c.id = rc.category_id AND c.name = $4
		`
		args = append(args, category)
	}

	query += `
		WHERE ST_DWithin(
		    ST_MakePoint(r.longitude, r.latitude)::geography,
		    ST_MakePoint($1, $2)::geography,
		    $3
		)
	`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type restaurantWithID struct {
		id int64
		r  models.Restaurant
	}
	var rows2 []restaurantWithID
	for rows.Next() {
		var id int64
		var r models.Restaurant
		err := rows.Scan(
			&r.Name,
			&r.Address,
			&r.URL,
			&r.Rating,
			&r.Latitude,
			&r.Longitude,
			&r.LunchMinPrice,
			&r.LunchMaxPrice,
			&r.DinnerMinPrice,
			&r.DinnerMaxPrice,
			&r.NearestStation,
			&r.City,
			&r.Kids,
			&id,
		)
		if err != nil {
			return nil, err
		}
		rows2 = append(rows2, restaurantWithID{id, r})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(rows2) == 0 {
		return nil, nil
	}

	// Fetch categories for all restaurants in one query
	ids := make([]interface{}, len(rows2))
	placeholders := make([]string, len(rows2))
	for i, rr := range rows2 {
		ids[i] = rr.id
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	catQuery := fmt.Sprintf(`
		SELECT rc.restaurant_id, c.name
		FROM restaurant_categories rc
		JOIN categories c ON c.id = rc.category_id
		WHERE rc.restaurant_id IN (%s)
	`, strings.Join(placeholders, ","))

	catRows, err := db.Query(catQuery, ids...)
	if err != nil {
		return nil, err
	}
	defer catRows.Close()

	catMap := make(map[int64][]string)
	for catRows.Next() {
		var rid int64
		var name string
		if err := catRows.Scan(&rid, &name); err != nil {
			return nil, err
		}
		catMap[rid] = append(catMap[rid], name)
	}
	if err := catRows.Err(); err != nil {
		return nil, err
	}

	restaurants := make([]models.Restaurant, len(rows2))
	for i, rr := range rows2 {
		rr.r.Categories = catMap[rr.id]
		restaurants[i] = rr.r
	}
	return restaurants, nil
}
