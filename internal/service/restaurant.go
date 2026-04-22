package service

import (
	"database/sql"

	"tabelog-map/internal/db"
	"tabelog-map/models"
)

const nearbyRadiusMeters = 3000

func GetNearbyRestaurants(conn *sql.DB, lat, lng float64, category string) ([]models.Restaurant, error) {
	return db.GetNearbyRestaurants(conn, lat, lng, nearbyRadiusMeters, category)
}

func GetCategories(conn *sql.DB) ([]string, error) {
	return db.GetCategories(conn)
}

func SearchMenuItems(conn *sql.DB, query string) ([]models.MenuSearchResult, error) {
	return db.SearchMenuItems(conn, query, 50)
}
