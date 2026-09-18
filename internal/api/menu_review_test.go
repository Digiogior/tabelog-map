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

func TestGetNextMenuReviewSuccess(t *testing.T) {
	fetch := func() (*models.MenuReviewTask, error) {
		return &models.MenuReviewTask{
			ID:             9,
			RestaurantName: "Test restaurant",
			ImageURL:       "https://example.com/menu.jpg",
			Items: []models.MenuReviewItem{
				{ID: 1, ExtractedName: "特上カルビ", Decision: "pending", EvidenceBlockIDs: []string{"p0_b0"}},
			},
		}, nil
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/menu-reviews/next", nil)
	handleGetNextMenuReview(fetch)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	var task models.MenuReviewTask
	if err := json.NewDecoder(recorder.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.ID != 9 || len(task.Items) != 1 || task.Items[0].ExtractedName != "特上カルビ" {
		t.Fatalf("unexpected task: %#v", task)
	}
}

func TestGetNextMenuReviewEmpty(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/menu-reviews/next", nil)
	handleGetNextMenuReview(func() (*models.MenuReviewTask, error) {
		return nil, sql.ErrNoRows
	})(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", recorder.Code)
	}
}

func TestSaveMenuReviewPassesValidatedPayload(t *testing.T) {
	body := []byte(`{"items":[{"id":4,"decision":"edited","reviewed_name":"タン塩"}],"note":"fixed OCR"}`)
	var savedTaskID int64
	var saved models.MenuReviewSubmission
	save := func(taskID int64, submission models.MenuReviewSubmission) error {
		savedTaskID = taskID
		saved = submission
		return nil
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/menu-reviews/12", bytes.NewReader(body))
	handleSaveMenuReview(save)(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if savedTaskID != 12 || len(saved.Items) != 1 || saved.Items[0].ReviewedName != "タン塩" {
		t.Fatalf("unexpected saved submission: task=%d payload=%#v", savedTaskID, saved)
	}
}

func TestSaveMenuReviewRejectsInvalidTaskID(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/menu-reviews/not-a-number", bytes.NewReader([]byte(`{}`)))
	handleSaveMenuReview(func(int64, models.MenuReviewSubmission) error { return nil })(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

func TestSaveMenuReviewMapsDomainErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"not found", db.ErrMenuReviewNotFound, http.StatusNotFound},
		{"invalid", errors.Join(db.ErrInvalidMenuReview, errors.New("missing decisions")), http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/menu-reviews/3", bytes.NewReader([]byte(`{"items":[]}`)))
			handleSaveMenuReview(func(int64, models.MenuReviewSubmission) error {
				return test.err
			})(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("expected %d, got %d", test.status, recorder.Code)
			}
		})
	}
}
