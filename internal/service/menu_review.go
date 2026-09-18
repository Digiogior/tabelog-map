package service

import (
	"database/sql"

	"tabelog-map/internal/db"
	"tabelog-map/models"
)

func GetNextMenuReview(conn *sql.DB) (*models.MenuReviewTask, error) {
	return db.GetNextMenuReview(conn)
}

func GetMenuReviewStats(conn *sql.DB) (models.MenuReviewStats, error) {
	return db.GetMenuReviewStats(conn)
}

func SaveMenuReview(conn *sql.DB, taskID int64, submission models.MenuReviewSubmission) error {
	return db.SaveMenuReview(conn, taskID, submission)
}
