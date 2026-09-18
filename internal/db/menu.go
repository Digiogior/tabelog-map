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
		`ALTER TABLE menu_items ADD COLUMN IF NOT EXISTS name_ja TEXT`,
		`ALTER TABLE menu_items ADD COLUMN IF NOT EXISTS description_ja TEXT`,
		`CREATE TABLE IF NOT EXISTS menu_scrape_log (
			restaurant_id BIGINT PRIMARY KEY REFERENCES restaurants(id) ON DELETE CASCADE,
			status        TEXT NOT NULL,
			error_msg     TEXT,
			retry_count   INTEGER NOT NULL DEFAULT 0,
			attempted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			completed_at  TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS menu_review_tasks (
			id              BIGSERIAL PRIMARY KEY,
			restaurant_id   BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
			image_url       TEXT NOT NULL,
			source_page_url TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'pending'
			                CHECK (status IN ('pending', 'completed')),
			reviewer_note   TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			reviewed_at     TIMESTAMPTZ,
			UNIQUE (restaurant_id, image_url)
		)`,
		`CREATE INDEX IF NOT EXISTS menu_review_tasks_status_id_idx
		 ON menu_review_tasks(status, id)`,
		`CREATE TABLE IF NOT EXISTS menu_review_items (
			id                 BIGSERIAL PRIMARY KEY,
			review_task_id     BIGINT NOT NULL REFERENCES menu_review_tasks(id) ON DELETE CASCADE,
			extracted_name     TEXT NOT NULL,
			reviewed_name      TEXT,
			decision           TEXT NOT NULL DEFAULT 'pending'
			                   CHECK (decision IN ('pending', 'approved', 'rejected', 'edited')),
			evidence_block_ids JSONB NOT NULL DEFAULT '[]'::JSONB,
			created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			reviewed_at        TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS menu_review_items_task_id_idx
		 ON menu_review_items(review_task_id, id)`,
		`ALTER TABLE menu_items ADD COLUMN IF NOT EXISTS review_item_id BIGINT
		 REFERENCES menu_review_items(id) ON DELETE SET NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS menu_items_review_item_id_idx
		 ON menu_items(review_item_id) WHERE review_item_id IS NOT NULL`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return CreateFoodCategoryTables(db)
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
