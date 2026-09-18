package service

import (
	"database/sql"

	"tabelog-map/internal/db"
	"tabelog-map/models"
)

func ListFoodTypes(conn *sql.DB) ([]models.FoodType, error) {
	return db.ListFoodTypes(conn)
}

func GetNextFoodCategoryReview(conn *sql.DB) (*models.FoodCategoryReviewTask, error) {
	return db.GetNextFoodCategoryReview(conn)
}

func GetFoodCategoryReviewStats(conn *sql.DB) (models.FoodCategoryReviewStats, error) {
	return db.GetFoodCategoryReviewStats(conn)
}

func SaveFoodCategoryReview(conn *sql.DB, taskID int64, submission models.FoodCategoryReviewSubmission) error {
	return db.SaveFoodCategoryReview(conn, taskID, submission)
}

func SearchRestaurantsByFoodType(conn *sql.DB, query string) ([]models.FoodTypeSearchResult, error) {
	return db.SearchRestaurantsByFoodType(conn, query, 50)
}
