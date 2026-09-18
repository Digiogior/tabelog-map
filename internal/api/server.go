package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"

	"tabelog-map/internal/service"
	"tabelog-map/models"
)

type restaurantFetcher func(lat, lng float64, category, prefecture string) ([]models.Restaurant, error)
type categoryFetcher func() ([]string, error)
type menuSearcher func(query string) ([]models.MenuSearchResult, error)

func StartServer(conn *sql.DB) {
	fetcher := func(lat, lng float64, category, prefecture string) ([]models.Restaurant, error) {
		return service.GetNearbyRestaurants(conn, lat, lng, category, prefecture)
	}
	catFetcher := func() ([]string, error) {
		return service.GetCategories(conn)
	}
	topCatFetcher := func() ([]string, error) {
		return service.GetTopCategories(conn)
	}
	menuFetcher := func(query string) ([]models.MenuSearchResult, error) {
		return service.SearchMenuItems(conn, query)
	}
	reviewFetcher := func() (*models.MenuReviewTask, error) {
		return service.GetNextMenuReview(conn)
	}
	reviewStatsFetcher := func() (models.MenuReviewStats, error) {
		return service.GetMenuReviewStats(conn)
	}
	reviewSaver := func(taskID int64, submission models.MenuReviewSubmission) error {
		return service.SaveMenuReview(conn, taskID, submission)
	}
	foodTypesFetcher := func() ([]models.FoodType, error) {
		return service.ListFoodTypes(conn)
	}
	foodTypeSearch := func(query string) ([]models.FoodTypeSearchResult, error) {
		return service.SearchRestaurantsByFoodType(conn, query)
	}
	foodCategoryReviewFetcher := func() (*models.FoodCategoryReviewTask, error) {
		return service.GetNextFoodCategoryReview(conn)
	}
	foodCategoryReviewStatsFetcher := func() (models.FoodCategoryReviewStats, error) {
		return service.GetFoodCategoryReviewStats(conn)
	}
	foodCategoryReviewSaver := func(taskID int64, submission models.FoodCategoryReviewSubmission) error {
		return service.SaveFoodCategoryReview(conn, taskID, submission)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/restaurants", handleGetRestaurants(fetcher))
	mux.HandleFunc("/api/categories", handleGetCategories(catFetcher))
	mux.HandleFunc("/api/categories/top", handleGetCategories(topCatFetcher))
	mux.HandleFunc("/api/menu-search", handleMenuSearch(menuFetcher))
	mux.HandleFunc("/api/menu-reviews/next", handleGetNextMenuReview(reviewFetcher))
	mux.HandleFunc("/api/menu-reviews/stats", handleGetMenuReviewStats(reviewStatsFetcher))
	mux.HandleFunc("/api/menu-reviews/", handleSaveMenuReview(reviewSaver))
	mux.HandleFunc("/api/food-types", handleGetFoodTypes(foodTypesFetcher))
	mux.HandleFunc("/api/food-type-search", handleFoodTypeSearch(foodTypeSearch))
	mux.HandleFunc("/api/food-category-reviews/next", handleGetNextFoodCategoryReview(foodCategoryReviewFetcher))
	mux.HandleFunc("/api/food-category-reviews/stats", handleGetFoodCategoryReviewStats(foodCategoryReviewStatsFetcher))
	mux.HandleFunc("/api/food-category-reviews/", handleSaveFoodCategoryReview(foodCategoryReviewSaver))
	mux.Handle("/", http.FileServer(http.Dir("map")))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	address := ":" + port
	log.Printf("API server listening on %s", address)
	if err := http.ListenAndServe(address, mux); err != nil {
		log.Fatal(err)
	}
}

func handleGetRestaurants(fetch restaurantFetcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")

		latStr := r.URL.Query().Get("lat")
		lngStr := r.URL.Query().Get("lng")
		category := r.URL.Query().Get("category")
		prefecture := r.URL.Query().Get("prefecture")

		lat, err := strconv.ParseFloat(latStr, 64)
		if err != nil {
			http.Error(w, "invalid lat", http.StatusBadRequest)
			return
		}

		lng, err := strconv.ParseFloat(lngStr, 64)
		if err != nil {
			http.Error(w, "invalid lng", http.StatusBadRequest)
			return
		}

		restaurants, err := fetch(lat, lng, category, prefecture)
		if err != nil {
			log.Println("error fetching restaurants:", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if restaurants == nil {
			restaurants = []models.Restaurant{}
		}

		json.NewEncoder(w).Encode(restaurants)
	}
}

func handleGetCategories(fetch categoryFetcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")

		categories, err := fetch()
		if err != nil {
			log.Println("error fetching categories:", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if categories == nil {
			categories = []string{}
		}

		json.NewEncoder(w).Encode(categories)
	}
}

func handleMenuSearch(search menuSearcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")

		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, "missing query parameter: q", http.StatusBadRequest)
			return
		}

		results, err := search(q)
		if err != nil {
			log.Println("error searching menu items:", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if results == nil {
			results = []models.MenuSearchResult{}
		}

		json.NewEncoder(w).Encode(results)
	}
}
