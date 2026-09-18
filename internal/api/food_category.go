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

type foodTypeFetcher func() ([]models.FoodType, error)
type foodTypeSearcher func(query string) ([]models.FoodTypeSearchResult, error)
type foodCategoryReviewFetcher func() (*models.FoodCategoryReviewTask, error)
type foodCategoryReviewStatsFetcher func() (models.FoodCategoryReviewStats, error)
type foodCategoryReviewSaver func(taskID int64, submission models.FoodCategoryReviewSubmission) error

func handleGetFoodTypes(fetch foodTypeFetcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		foodTypes, err := fetch()
		if err != nil {
			log.Println("error fetching food types:", err)
			writeJSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if foodTypes == nil {
			foodTypes = []models.FoodType{}
		}
		if err := json.NewEncoder(w).Encode(foodTypes); err != nil {
			log.Println("error encoding food types:", err)
		}
	}
}

func handleFoodTypeSearch(search foodTypeSearcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" {
			writeJSONError(w, "missing query parameter: q", http.StatusBadRequest)
			return
		}
		results, err := search(query)
		if err != nil {
			log.Println("error searching restaurant food types:", err)
			writeJSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if results == nil {
			results = []models.FoodTypeSearchResult{}
		}
		if err := json.NewEncoder(w).Encode(results); err != nil {
			log.Println("error encoding restaurant food type search:", err)
		}
	}
}

func handleGetNextFoodCategoryReview(fetch foodCategoryReviewFetcher) http.HandlerFunc {
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
			log.Println("error fetching next food category review:", err)
			writeJSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(task); err != nil {
			log.Println("error encoding food category review:", err)
		}
	}
}

func handleGetFoodCategoryReviewStats(fetch foodCategoryReviewStatsFetcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		stats, err := fetch()
		if err != nil {
			log.Println("error fetching food category review stats:", err)
			writeJSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(stats); err != nil {
			log.Println("error encoding food category review stats:", err)
		}
	}
}

func handleSaveFoodCategoryReview(save foodCategoryReviewSaver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/food-category-reviews/"), "/")
		if path == "" || strings.Contains(path, "/") {
			writeJSONError(w, "invalid review task id", http.StatusBadRequest)
			return
		}
		taskID, err := strconv.ParseInt(path, 10, 64)
		if err != nil || taskID <= 0 {
			writeJSONError(w, "invalid review task id", http.StatusBadRequest)
			return
		}

		var submission models.FoodCategoryReviewSubmission
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&submission); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := save(taskID, submission); err != nil {
			switch {
			case errors.Is(err, db.ErrFoodCategoryReviewNotFound):
				writeJSONError(w, "review task not found", http.StatusNotFound)
			case errors.Is(err, db.ErrInvalidFoodCategoryReview):
				writeJSONError(w, err.Error(), http.StatusBadRequest)
			default:
				log.Println("error saving food category review:", err)
				writeJSONError(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
