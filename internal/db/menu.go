package db

import (
	"database/sql"

	"tabelog-map/models"
)

func CreateMenuTables(db *sql.DB) error {
	statements := []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		`CREATE TABLE IF NOT EXISTS menu_items (
			id            BIGSERIAL PRIMARY KEY,
			restaurant_id BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
			name          TEXT NOT NULL,
			price         INTEGER,
			price_bottle  INTEGER,
			description   TEXT,
			scraped_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS menu_items_restaurant_id_idx ON menu_items(restaurant_id)`,
		`CREATE INDEX IF NOT EXISTS menu_items_name_trgm_idx ON menu_items USING GIN (name gin_trgm_ops)`,
		`CREATE TABLE IF NOT EXISTS menu_scrape_log (
			restaurant_id BIGINT PRIMARY KEY REFERENCES restaurants(id) ON DELETE CASCADE,
			status        TEXT NOT NULL,
			error_msg     TEXT,
			retry_count   INTEGER NOT NULL DEFAULT 0,
			attempted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			completed_at  TIMESTAMPTZ
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func SearchMenuItems(db *sql.DB, query string, limit int) ([]models.MenuSearchResult, error) {
	rows, err := db.Query(`
		SELECT r.name, r.address, r.url, r.rating, r.latitude, r.longitude,
		       m.name, m.price, m.description
		FROM menu_items m
		JOIN restaurants r ON r.id = m.restaurant_id
		WHERE m.name % $1 OR m.name ILIKE $2
		ORDER BY similarity(m.name, $1) DESC
		LIMIT $3
	`, query, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.MenuSearchResult
	for rows.Next() {
		var res models.MenuSearchResult
		var price sql.NullInt64
		var desc sql.NullString
		if err := rows.Scan(
			&res.RestaurantName,
			&res.RestaurantAddress,
			&res.RestaurantURL,
			&res.RestaurantRating,
			&res.Latitude,
			&res.Longitude,
			&res.ItemName,
			&price,
			&desc,
		); err != nil {
			return nil, err
		}
		if price.Valid {
			p := int(price.Int64)
			res.Price = &p
		}
		if desc.Valid {
			res.Description = &desc.String
		}
		results = append(results, res)
	}
	return results, rows.Err()
}
