package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tabelog-map/models"
)

var (
	ErrMenuReviewNotFound = errors.New("menu review task not found")
	ErrInvalidMenuReview  = errors.New("invalid menu review submission")
)

func GetNextMenuReview(db *sql.DB) (*models.MenuReviewTask, error) {
	var task models.MenuReviewTask
	err := db.QueryRow(`
		SELECT t.id, t.restaurant_id, COALESCE(r.name_ja, r.name, ''),
		       COALESCE(r.url, ''), t.image_url, t.source_page_url
		FROM menu_review_tasks t
		JOIN restaurants r ON r.id = t.restaurant_id
		WHERE t.status = 'pending'
		ORDER BY t.id
		LIMIT 1
	`).Scan(
		&task.ID,
		&task.RestaurantID,
		&task.RestaurantName,
		&task.RestaurantURL,
		&task.ImageURL,
		&task.SourcePageURL,
	)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT id, extracted_name, reviewed_name, decision, evidence_block_ids
		FROM menu_review_items
		WHERE review_task_id = $1
		ORDER BY id
	`, task.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item models.MenuReviewItem
		var reviewedName sql.NullString
		var evidenceJSON []byte
		if err := rows.Scan(
			&item.ID,
			&item.ExtractedName,
			&reviewedName,
			&item.Decision,
			&evidenceJSON,
		); err != nil {
			return nil, err
		}
		if reviewedName.Valid {
			item.ReviewedName = &reviewedName.String
		}
		if err := json.Unmarshal(evidenceJSON, &item.EvidenceBlockIDs); err != nil {
			return nil, fmt.Errorf("decode evidence for review item %d: %w", item.ID, err)
		}
		if item.EvidenceBlockIDs == nil {
			item.EvidenceBlockIDs = []string{}
		}
		task.Items = append(task.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if task.Items == nil {
		task.Items = []models.MenuReviewItem{}
	}
	return &task, nil
}

func GetMenuReviewStats(db *sql.DB) (models.MenuReviewStats, error) {
	var stats models.MenuReviewStats
	err := db.QueryRow(`
		SELECT
			COUNT(*) FILTER (WHERE status = 'pending'),
			COUNT(*) FILTER (WHERE status = 'completed'),
			(
				SELECT COUNT(*)
				FROM menu_review_items i
				JOIN menu_review_tasks task ON task.id = i.review_task_id
				WHERE task.status = 'pending' AND i.decision = 'pending'
			)
		FROM menu_review_tasks
	`).Scan(&stats.PendingTasks, &stats.CompletedTasks, &stats.PendingItems)
	return stats, err
}

func SaveMenuReview(db *sql.DB, taskID int64, submission models.MenuReviewSubmission) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var restaurantID int64
	var status string
	err = tx.QueryRow(`
		SELECT restaurant_id, status
		FROM menu_review_tasks
		WHERE id = $1
		FOR UPDATE
	`, taskID).Scan(&restaurantID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMenuReviewNotFound
	}
	if err != nil {
		return err
	}
	if status != "pending" {
		return fmt.Errorf("%w: task is already completed", ErrInvalidMenuReview)
	}

	rows, err := tx.Query(`
		SELECT id, extracted_name
		FROM menu_review_items
		WHERE review_task_id = $1
		ORDER BY id
		FOR UPDATE
	`, taskID)
	if err != nil {
		return err
	}

	extractedNames := make(map[int64]string)
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		extractedNames[id] = name
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(extractedNames) == 0 {
		return fmt.Errorf("%w: task has no items", ErrInvalidMenuReview)
	}
	if len(submission.Items) != len(extractedNames) {
		return fmt.Errorf("%w: every item must be reviewed", ErrInvalidMenuReview)
	}

	seen := make(map[int64]bool, len(submission.Items))
	for _, decision := range submission.Items {
		extractedName, ok := extractedNames[decision.ID]
		if !ok || seen[decision.ID] {
			return fmt.Errorf("%w: unknown or duplicate item %d", ErrInvalidMenuReview, decision.ID)
		}
		seen[decision.ID] = true

		reviewedName := strings.TrimSpace(decision.ReviewedName)
		switch decision.Decision {
		case "approved":
			reviewedName = extractedName
		case "edited":
			if reviewedName == "" {
				return fmt.Errorf("%w: edited item %d needs a name", ErrInvalidMenuReview, decision.ID)
			}
		case "rejected":
			reviewedName = ""
		default:
			return fmt.Errorf("%w: unsupported decision %q", ErrInvalidMenuReview, decision.Decision)
		}

		var reviewedNameValue any
		if reviewedName != "" {
			reviewedNameValue = reviewedName
		}
		if _, err := tx.Exec(`
			UPDATE menu_review_items
			SET decision = $1, reviewed_name = $2, reviewed_at = NOW()
			WHERE id = $3 AND review_task_id = $4
		`, decision.Decision, reviewedNameValue, decision.ID, taskID); err != nil {
			return err
		}

		if decision.Decision == "rejected" {
			if _, err := tx.Exec(`DELETE FROM menu_items WHERE review_item_id = $1`, decision.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO menu_items (restaurant_id, name, review_item_id, scraped_at)
			VALUES ($1, $2, $3, NOW())
			ON CONFLICT (review_item_id) WHERE review_item_id IS NOT NULL
			DO UPDATE SET name = EXCLUDED.name, scraped_at = NOW()
		`, restaurantID, reviewedName, decision.ID); err != nil {
			return err
		}
	}

	var note any
	if value := strings.TrimSpace(submission.Note); value != "" {
		note = value
	}
	if _, err := tx.Exec(`
		UPDATE menu_review_tasks
		SET status = 'completed', reviewer_note = $1, reviewed_at = NOW()
		WHERE id = $2
	`, note, taskID); err != nil {
		return err
	}

	return tx.Commit()
}
