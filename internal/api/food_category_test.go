package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"tabelog-map/internal/db"
	"tabelog-map/models"
)

func TestGetNextFoodCategoryReviewSuccess(t *testing.T) {
	fetch := func() (*models.FoodCategoryReviewTask, error) {
		return &models.FoodCategoryReviewTask{
			ID:             4,
			RestaurantName: "Test restaurant",
			Photos:         []string{"https://example.com/menu.jpg"},
			Candidates: []models.FoodCategoryReviewCandidate{{
				ID:                8,
				SuggestedFoodType: &models.FoodType{ID: 2, Slug: "salad", NameEN: "Salad"},
				Decision:          "pending",
			}},
		}, nil
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/food-category-reviews/next", nil)
	handleGetNextFoodCategoryReview(fetch)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	var task models.FoodCategoryReviewTask
	if err := json.NewDecoder(recorder.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.ID != 4 || len(task.Candidates) != 1 || task.Candidates[0].SuggestedFoodType.Slug != "salad" {
		t.Fatalf("unexpected task: %#v", task)
	}
}

func TestGetNextFoodCategoryReviewEmpty(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/food-category-reviews/next", nil)
	handleGetNextFoodCategoryReview(func() (*models.FoodCategoryReviewTask, error) {
		return nil, sql.ErrNoRows
	})(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", recorder.Code)
	}
}

func TestSaveFoodCategoryReviewPassesPayload(t *testing.T) {
	body := []byte(`{"candidates":[{"id":8,"decision":"changed","food_type_id":3}],"additions":[{"food_type_id":5}],"proposals":[{"proposed_name":"Rice paper rolls","explanation":"No matching category"}],"note":"checked"}`)
	var savedTaskID int64
	var saved models.FoodCategoryReviewSubmission
	save := func(taskID int64, submission models.FoodCategoryReviewSubmission) error {
		savedTaskID = taskID
		saved = submission
		return nil
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/food-category-reviews/12", bytes.NewReader(body))
	handleSaveFoodCategoryReview(save)(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if savedTaskID != 12 || saved.Candidates[0].FoodTypeID != 3 || saved.Proposals[0].ProposedName != "Rice paper rolls" {
		t.Fatalf("unexpected submission: task=%d payload=%#v", savedTaskID, saved)
	}
}

func TestSaveFoodCategoryReviewMapsDomainErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"not found", db.ErrFoodCategoryReviewNotFound, http.StatusNotFound},
		{"invalid", errors.Join(db.ErrInvalidFoodCategoryReview, errors.New("missing decisions")), http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/food-category-reviews/3", bytes.NewReader([]byte(`{"candidates":[]}`)))
			handleSaveFoodCategoryReview(func(int64, models.FoodCategoryReviewSubmission) error {
				return test.err
			})(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("expected %d, got %d", test.status, recorder.Code)
			}
		})
	}
}

func TestFoodTypeSearchRequiresQuery(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/food-type-search", nil)
	handleFoodTypeSearch(func(string) ([]models.FoodTypeSearchResult, error) {
		return nil, nil
	})(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}
