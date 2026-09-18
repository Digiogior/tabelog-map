package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"tabelog-map/internal/db"
	"tabelog-map/models"
)

type menuReviewFetcher func() (*models.MenuReviewTask, error)
type menuReviewStatsFetcher func() (models.MenuReviewStats, error)
type menuReviewSaver func(taskID int64, submission models.MenuReviewSubmission) error

func handleGetNextMenuReview(fetch menuReviewFetcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		task, err := fetch()
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			log.Println("error fetching next menu review:", err)
			writeJSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(task); err != nil {
			log.Println("error encoding menu review:", err)
		}
	}
}

func handleGetMenuReviewStats(fetch menuReviewStatsFetcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		stats, err := fetch()
		if err != nil {
			log.Println("error fetching menu review stats:", err)
			writeJSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(stats); err != nil {
			log.Println("error encoding menu review stats:", err)
		}
	}
}

func handleSaveMenuReview(save menuReviewSaver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/menu-reviews/"), "/")
		if path == "" || strings.Contains(path, "/") {
			writeJSONError(w, "invalid review task id", http.StatusBadRequest)
			return
		}
		taskID, err := strconv.ParseInt(path, 10, 64)
		if err != nil || taskID <= 0 {
			writeJSONError(w, "invalid review task id", http.StatusBadRequest)
			return
		}

		var submission models.MenuReviewSubmission
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&submission); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := save(taskID, submission); err != nil {
			switch {
			case errors.Is(err, db.ErrMenuReviewNotFound):
				writeJSONError(w, "review task not found", http.StatusNotFound)
			case errors.Is(err, db.ErrInvalidMenuReview):
				writeJSONError(w, err.Error(), http.StatusBadRequest)
			default:
				log.Println("error saving menu review:", err)
				writeJSONError(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
