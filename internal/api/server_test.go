package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"tabelog-map/models"
)

var stubRestaurants = []models.Restaurant{
	{
		Name:      "Test Ramen",
		Address:   "1-1 Nagoya",
		URL:       "https://tabelog.com/test",
		Rating:    3.8,
		Latitude:  35.17,
		Longitude: 136.90,
	},
}

func TestGetRestaurants_Success(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return stubRestaurants, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=35.17&lng=136.90", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}

	var restaurants []models.Restaurant
	if err := json.NewDecoder(res.Body).Decode(&restaurants); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(restaurants) != 1 {
		t.Errorf("expected 1 restaurant, got %d", len(restaurants))
	}

	if restaurants[0].Name != "Test Ramen" {
		t.Errorf("expected name 'Test Ramen', got '%s'", restaurants[0].Name)
	}
}

func TestGetRestaurants_EmptyResult(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return nil, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=35.17&lng=136.90", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}

	var restaurants []models.Restaurant
	if err := json.NewDecoder(res.Body).Decode(&restaurants); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(restaurants) != 0 {
		t.Errorf("expected empty array, got %d items", len(restaurants))
	}
}

func TestGetRestaurants_MissingLat(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return stubRestaurants, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lng=136.90", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestGetRestaurants_MissingLng(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return stubRestaurants, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=35.17", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestGetRestaurants_InvalidLat(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return stubRestaurants, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=notanumber&lng=136.90", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestGetRestaurants_InvalidLng(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return stubRestaurants, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=35.17&lng=notanumber", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestGetRestaurants_DBError(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return nil, errors.New("db connection failed")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=35.17&lng=136.90", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	if w.Result().StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Result().StatusCode)
	}
}

func TestGetRestaurants_CORSHeader(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return stubRestaurants, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=35.17&lng=136.90", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	cors := w.Result().Header.Get("Access-Control-Allow-Origin")
	if cors != "*" {
		t.Errorf("expected CORS header '*', got '%s'", cors)
	}
}

func TestGetRestaurants_ContentTypeHeader(t *testing.T) {
	fetcher := func(lat, lng float64) ([]models.Restaurant, error) {
		return stubRestaurants, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/restaurants?lat=35.17&lng=136.90", nil)
	w := httptest.NewRecorder()

	handleGetRestaurants(fetcher)(w, req)

	ct := w.Result().Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got '%s'", ct)
	}
}
