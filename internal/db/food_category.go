package db

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tabelog-map/models"
)

var (
	ErrFoodCategoryReviewNotFound = errors.New("food category review task not found")
	ErrInvalidFoodCategoryReview  = errors.New("invalid food category review submission")
)

//go:embed food_taxonomy.json
var foodTaxonomyJSON []byte

type foodTypeSeed struct {
	Slug        string   `json:"slug"`
	NameEN      string   `json:"name_en"`
	NameJA      string   `json:"name_ja"`
	NameZH      string   `json:"name_zh"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
	ParentSlug  string   `json:"parent_slug"`
}

func validateFoodTypeSeeds(seeds []foodTypeSeed) error {
	bySlug := make(map[string]foodTypeSeed, len(seeds))
	for _, seed := range seeds {
		if seed.Slug == "" {
			return errors.New("food taxonomy contains an empty slug")
		}
		if _, exists := bySlug[seed.Slug]; exists {
			return fmt.Errorf("food taxonomy contains duplicate slug %q", seed.Slug)
		}
		bySlug[seed.Slug] = seed
	}
	for _, seed := range seeds {
		if seed.ParentSlug == "" {
			continue
		}
		if seed.ParentSlug == seed.Slug {
			return fmt.Errorf("food type %q cannot be its own parent", seed.Slug)
		}
		if _, exists := bySlug[seed.ParentSlug]; !exists {
			return fmt.Errorf("food type %q has unknown parent %q", seed.Slug, seed.ParentSlug)
		}
	}

	state := make(map[string]uint8, len(seeds))
	var visit func(string) error
	visit = func(slug string) error {
		switch state[slug] {
		case 1:
			return fmt.Errorf("food taxonomy contains a parent cycle at %q", slug)
		case 2:
			return nil
		}
		state[slug] = 1
		if parent := bySlug[slug].ParentSlug; parent != "" {
			if err := visit(parent); err != nil {
				return err
			}
		}
		state[slug] = 2
		return nil
	}
	for slug := range bySlug {
		if err := visit(slug); err != nil {
			return err
		}
	}
	return nil
}

func CreateFoodCategoryTables(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS food_types (
			id          BIGSERIAL PRIMARY KEY,
			slug        TEXT UNIQUE NOT NULL,
			name_en     TEXT NOT NULL,
			name_ja     TEXT NOT NULL,
			name_zh     TEXT NOT NULL,
			aliases     JSONB NOT NULL DEFAULT '[]'::JSONB,
			description TEXT NOT NULL DEFAULT '',
			parent_id   BIGINT REFERENCES food_types(id) ON DELETE SET NULL,
			active      BOOLEAN NOT NULL DEFAULT TRUE,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS food_types_slug_trgm_idx
		 ON food_types USING GIN (slug gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS food_types_name_en_trgm_idx
		 ON food_types USING GIN (name_en gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS food_types_name_ja_trgm_idx
		 ON food_types USING GIN (name_ja gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS food_types_name_zh_trgm_idx
		 ON food_types USING GIN (name_zh gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS food_types_parent_idx
		 ON food_types(parent_id, id)`,
		`CREATE TABLE IF NOT EXISTS food_category_review_tasks (
			id              BIGSERIAL PRIMARY KEY,
			restaurant_id   BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
			status          TEXT NOT NULL DEFAULT 'pending'
			                CHECK (status IN ('pending', 'completed')),
			reviewer_note   TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			reviewed_at     TIMESTAMPTZ
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS food_category_review_tasks_pending_restaurant_idx
		 ON food_category_review_tasks(restaurant_id) WHERE status = 'pending'`,
		`CREATE INDEX IF NOT EXISTS food_category_review_tasks_status_id_idx
		 ON food_category_review_tasks(status, id)`,
		`CREATE TABLE IF NOT EXISTS food_category_review_photos (
			id              BIGSERIAL PRIMARY KEY,
			review_task_id  BIGINT NOT NULL REFERENCES food_category_review_tasks(id) ON DELETE CASCADE,
			image_url       TEXT NOT NULL,
			source_page_url TEXT NOT NULL DEFAULT '',
			position        INTEGER NOT NULL DEFAULT 0,
			UNIQUE (review_task_id, image_url)
		)`,
		`CREATE TABLE IF NOT EXISTS food_category_review_candidates (
			id                     BIGSERIAL PRIMARY KEY,
			review_task_id         BIGINT NOT NULL REFERENCES food_category_review_tasks(id) ON DELETE CASCADE,
			suggested_food_type_id BIGINT REFERENCES food_types(id) ON DELETE RESTRICT,
			reviewed_food_type_id  BIGINT REFERENCES food_types(id) ON DELETE RESTRICT,
			decision               TEXT NOT NULL DEFAULT 'pending'
			                       CHECK (decision IN ('pending', 'approved', 'rejected', 'changed', 'added')),
			origin                 TEXT NOT NULL DEFAULT 'model'
			                       CHECK (origin IN ('model', 'reviewer')),
			created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			reviewed_at            TIMESTAMPTZ
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS food_category_review_candidates_suggested_idx
		 ON food_category_review_candidates(review_task_id, suggested_food_type_id)
		 WHERE suggested_food_type_id IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS food_category_review_evidence (
			id              BIGSERIAL PRIMARY KEY,
			candidate_id    BIGINT NOT NULL REFERENCES food_category_review_candidates(id) ON DELETE CASCADE,
			image_url       TEXT NOT NULL DEFAULT '',
			source_page_url TEXT NOT NULL DEFAULT '',
			evidence_text   TEXT NOT NULL,
			block_ids       JSONB NOT NULL DEFAULT '[]'::JSONB,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (candidate_id, image_url, evidence_text)
		)`,
		`CREATE INDEX IF NOT EXISTS food_category_review_evidence_candidate_idx
		 ON food_category_review_evidence(candidate_id, id)`,
		`CREATE TABLE IF NOT EXISTS restaurant_food_types (
			restaurant_id      BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
			food_type_id       BIGINT NOT NULL REFERENCES food_types(id) ON DELETE RESTRICT,
			review_candidate_id BIGINT REFERENCES food_category_review_candidates(id) ON DELETE SET NULL,
			provenance         TEXT NOT NULL CHECK (provenance IN ('model_reviewed', 'reviewer_added')),
			created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (restaurant_id, food_type_id)
		)`,
		`CREATE INDEX IF NOT EXISTS restaurant_food_types_food_type_idx
		 ON restaurant_food_types(food_type_id, restaurant_id)`,
		`CREATE TABLE IF NOT EXISTS food_taxonomy_proposals (
			id                    BIGSERIAL PRIMARY KEY,
			review_task_id        BIGINT NOT NULL REFERENCES food_category_review_tasks(id) ON DELETE CASCADE,
			restaurant_id         BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
			proposed_name         TEXT NOT NULL,
			parent_food_type_id   BIGINT REFERENCES food_types(id) ON DELETE SET NULL,
			explanation           TEXT NOT NULL DEFAULT '',
			evidence              JSONB NOT NULL DEFAULT '[]'::JSONB,
			status                TEXT NOT NULL DEFAULT 'pending'
			                      CHECK (status IN ('pending', 'created', 'mapped', 'rejected')),
			resolved_food_type_id BIGINT REFERENCES food_types(id) ON DELETE SET NULL,
			created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			resolved_at           TIMESTAMPTZ
		)`,
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}

	var seeds []foodTypeSeed
	if err := json.Unmarshal(foodTaxonomyJSON, &seeds); err != nil {
		return fmt.Errorf("decode food taxonomy: %w", err)
	}
	if err := validateFoodTypeSeeds(seeds); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, seed := range seeds {
		aliases, err := json.Marshal(seed.Aliases)
		if err != nil {
			return fmt.Errorf("encode aliases for %s: %w", seed.Slug, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO food_types (slug, name_en, name_ja, name_zh, aliases, description)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (slug) DO UPDATE SET
				name_en = EXCLUDED.name_en,
				name_ja = EXCLUDED.name_ja,
				name_zh = EXCLUDED.name_zh,
				aliases = EXCLUDED.aliases,
				description = EXCLUDED.description,
				active = TRUE,
				updated_at = NOW()
		`, seed.Slug, seed.NameEN, seed.NameJA, seed.NameZH, aliases, seed.Description); err != nil {
			return fmt.Errorf("seed food type %s: %w", seed.Slug, err)
		}
	}
	for _, seed := range seeds {
		if seed.ParentSlug == "" {
			if _, err := tx.Exec(`UPDATE food_types SET parent_id = NULL WHERE slug = $1`, seed.Slug); err != nil {
				return fmt.Errorf("clear parent for food type %s: %w", seed.Slug, err)
			}
			continue
		}
		if _, err := tx.Exec(`
			UPDATE food_types AS child
			SET parent_id = parent.id
			FROM food_types AS parent
			WHERE child.slug = $1 AND parent.slug = $2
		`, seed.Slug, seed.ParentSlug); err != nil {
			return fmt.Errorf("set parent for food type %s: %w", seed.Slug, err)
		}
	}
	return tx.Commit()
}

func scanFoodType(scanner interface{ Scan(...any) error }) (models.FoodType, error) {
	var foodType models.FoodType
	var aliasesJSON []byte
	err := scanner.Scan(
		&foodType.ID,
		&foodType.Slug,
		&foodType.NameEN,
		&foodType.NameJA,
		&foodType.NameZH,
		&aliasesJSON,
		&foodType.Description,
		&foodType.ParentID,
		&foodType.ParentSlug,
		&foodType.ParentNameEN,
		&foodType.ParentNameJA,
		&foodType.ParentNameZH,
	)
	if err != nil {
		return foodType, err
	}
	if err := json.Unmarshal(aliasesJSON, &foodType.Aliases); err != nil {
		return foodType, err
	}
	if foodType.Aliases == nil {
		foodType.Aliases = []string{}
	}
	return foodType, nil
}

func ListFoodTypes(db *sql.DB) ([]models.FoodType, error) {
	rows, err := db.Query(`
		SELECT f.id, f.slug, f.name_en, f.name_ja, f.name_zh, f.aliases, f.description,
		       COALESCE(f.parent_id, 0), COALESCE(parent.slug, ''),
		       COALESCE(parent.name_en, ''), COALESCE(parent.name_ja, ''),
		       COALESCE(parent.name_zh, '')
		FROM food_types f
		LEFT JOIN food_types parent ON parent.id = f.parent_id
		WHERE f.active = TRUE
		ORDER BY COALESCE(parent.name_en, f.name_en),
		         CASE WHEN f.parent_id IS NULL THEN 0 ELSE 1 END,
		         f.name_en, f.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	foodTypes := []models.FoodType{}
	for rows.Next() {
		foodType, err := scanFoodType(rows)
		if err != nil {
			return nil, err
		}
		foodTypes = append(foodTypes, foodType)
	}
	return foodTypes, rows.Err()
}

func GetNextFoodCategoryReview(db *sql.DB) (*models.FoodCategoryReviewTask, error) {
	var task models.FoodCategoryReviewTask
	err := db.QueryRow(`
		SELECT t.id, t.restaurant_id, COALESCE(r.name_ja, r.name, ''), COALESCE(r.url, '')
		FROM food_category_review_tasks t
		JOIN restaurants r ON r.id = t.restaurant_id
		WHERE t.status = 'pending'
		ORDER BY t.id
		LIMIT 1
	`).Scan(&task.ID, &task.RestaurantID, &task.RestaurantName, &task.RestaurantURL)
	if err != nil {
		return nil, err
	}
	photoRows, err := db.Query(`
		SELECT image_url
		FROM food_category_review_photos
		WHERE review_task_id = $1
		ORDER BY position, id
	`, task.ID)
	if err != nil {
		return nil, err
	}
	for photoRows.Next() {
		var imageURL string
		if err := photoRows.Scan(&imageURL); err != nil {
			photoRows.Close()
			return nil, err
		}
		task.Photos = append(task.Photos, imageURL)
	}
	if err := photoRows.Close(); err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT c.id, c.decision, c.origin,
		       f.id, f.slug, f.name_en, f.name_ja, f.name_zh, f.aliases, f.description,
		       COALESCE(f.parent_id, 0), COALESCE(parent.slug, ''),
		       COALESCE(parent.name_en, ''), COALESCE(parent.name_ja, ''),
		       COALESCE(parent.name_zh, '')
		FROM food_category_review_candidates c
		JOIN food_types f ON f.id = c.suggested_food_type_id
		LEFT JOIN food_types parent ON parent.id = f.parent_id
		WHERE c.review_task_id = $1 AND c.origin = 'model'
		ORDER BY COALESCE(parent.name_en, f.name_en),
		         CASE WHEN f.parent_id IS NULL THEN 0 ELSE 1 END,
		         f.name_en, c.id
	`, task.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var candidate models.FoodCategoryReviewCandidate
		var foodType models.FoodType
		var aliasesJSON []byte
		if err := rows.Scan(
			&candidate.ID,
			&candidate.Decision,
			&candidate.Origin,
			&foodType.ID,
			&foodType.Slug,
			&foodType.NameEN,
			&foodType.NameJA,
			&foodType.NameZH,
			&aliasesJSON,
			&foodType.Description,
			&foodType.ParentID,
			&foodType.ParentSlug,
			&foodType.ParentNameEN,
			&foodType.ParentNameJA,
			&foodType.ParentNameZH,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(aliasesJSON, &foodType.Aliases); err != nil {
			return nil, fmt.Errorf("decode aliases for %s: %w", foodType.Slug, err)
		}
		candidate.SuggestedFoodType = &foodType
		candidate.Evidence = []models.FoodCategoryEvidence{}
		task.Candidates = append(task.Candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	photoSeen := map[string]bool{}
	for _, imageURL := range task.Photos {
		photoSeen[imageURL] = true
	}
	for i := range task.Candidates {
		evidenceRows, err := db.Query(`
			SELECT id, image_url, source_page_url, evidence_text, block_ids
			FROM food_category_review_evidence
			WHERE candidate_id = $1
			ORDER BY id
		`, task.Candidates[i].ID)
		if err != nil {
			return nil, err
		}
		for evidenceRows.Next() {
			var evidence models.FoodCategoryEvidence
			var blockIDsJSON []byte
			if err := evidenceRows.Scan(
				&evidence.ID,
				&evidence.ImageURL,
				&evidence.SourcePageURL,
				&evidence.Text,
				&blockIDsJSON,
			); err != nil {
				evidenceRows.Close()
				return nil, err
			}
			if err := json.Unmarshal(blockIDsJSON, &evidence.BlockIDs); err != nil {
				evidenceRows.Close()
				return nil, fmt.Errorf("decode category evidence %d: %w", evidence.ID, err)
			}
			if evidence.BlockIDs == nil {
				evidence.BlockIDs = []string{}
			}
			task.Candidates[i].Evidence = append(task.Candidates[i].Evidence, evidence)
			if evidence.ImageURL != "" && !photoSeen[evidence.ImageURL] {
				photoSeen[evidence.ImageURL] = true
				task.Photos = append(task.Photos, evidence.ImageURL)
			}
		}
		if err := evidenceRows.Close(); err != nil {
			return nil, err
		}
	}
	if task.Candidates == nil {
		task.Candidates = []models.FoodCategoryReviewCandidate{}
	}
	if task.Photos == nil {
		task.Photos = []string{}
	}
	return &task, nil
}

func GetFoodCategoryReviewStats(db *sql.DB) (models.FoodCategoryReviewStats, error) {
	var stats models.FoodCategoryReviewStats
	err := db.QueryRow(`
		SELECT
			COUNT(*) FILTER (WHERE status = 'pending'),
			COUNT(*) FILTER (WHERE status = 'completed'),
			(
				SELECT COUNT(*)
				FROM food_category_review_candidates candidate
				JOIN food_category_review_tasks task ON task.id = candidate.review_task_id
				WHERE task.status = 'pending' AND candidate.decision = 'pending'
			)
		FROM food_category_review_tasks
	`).Scan(&stats.PendingTasks, &stats.CompletedTasks, &stats.PendingCategories)
	return stats, err
}

func activeFoodTypeExists(tx *sql.Tx, id int64) (bool, error) {
	var exists bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM food_types WHERE id = $1 AND active = TRUE)`, id).Scan(&exists)
	return exists, err
}

func foodTypesAreRelated(tx *sql.Tx, firstID, secondID int64) (bool, error) {
	var related bool
	err := tx.QueryRow(`
		WITH RECURSIVE first_lineage AS (
			SELECT id, parent_id FROM food_types WHERE id = $1
			UNION ALL
			SELECT parent.id, parent.parent_id
			FROM food_types parent
			JOIN first_lineage child ON child.parent_id = parent.id
		), second_lineage AS (
			SELECT id, parent_id FROM food_types WHERE id = $2
			UNION ALL
			SELECT parent.id, parent.parent_id
			FROM food_types parent
			JOIN second_lineage child ON child.parent_id = parent.id
		)
		SELECT EXISTS(SELECT 1 FROM first_lineage WHERE id = $2)
		    OR EXISTS(SELECT 1 FROM second_lineage WHERE id = $1)
	`, firstID, secondID).Scan(&related)
	return related, err
}

func registerPublishedFoodType(tx *sql.Tx, published map[int64]bool, foodTypeID int64) error {
	for existingID := range published {
		related, err := foodTypesAreRelated(tx, existingID, foodTypeID)
		if err != nil {
			return err
		}
		if related {
			return fmt.Errorf(
				"%w: category %d duplicates or overlaps ancestor/descendant category %d",
				ErrInvalidFoodCategoryReview, foodTypeID, existingID,
			)
		}
	}
	published[foodTypeID] = true
	return nil
}

func SaveFoodCategoryReview(db *sql.DB, taskID int64, submission models.FoodCategoryReviewSubmission) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var restaurantID int64
	var status string
	err = tx.QueryRow(`
		SELECT restaurant_id, status
		FROM food_category_review_tasks
		WHERE id = $1
		FOR UPDATE
	`, taskID).Scan(&restaurantID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFoodCategoryReviewNotFound
	}
	if err != nil {
		return err
	}
	if status != "pending" {
		return fmt.Errorf("%w: task is already completed", ErrInvalidFoodCategoryReview)
	}

	rows, err := tx.Query(`
		SELECT id, suggested_food_type_id
		FROM food_category_review_candidates
		WHERE review_task_id = $1 AND origin = 'model'
		ORDER BY id
		FOR UPDATE
	`, taskID)
	if err != nil {
		return err
	}
	suggested := map[int64]int64{}
	for rows.Next() {
		var candidateID, foodTypeID int64
		if err := rows.Scan(&candidateID, &foodTypeID); err != nil {
			rows.Close()
			return err
		}
		suggested[candidateID] = foodTypeID
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(submission.Candidates) != len(suggested) {
		return fmt.Errorf("%w: every category must be reviewed", ErrInvalidFoodCategoryReview)
	}

	seenCandidates := map[int64]bool{}
	publishedFoodTypes := map[int64]bool{}
	for _, decision := range submission.Candidates {
		suggestedFoodTypeID, ok := suggested[decision.ID]
		if !ok || seenCandidates[decision.ID] {
			return fmt.Errorf("%w: unknown or duplicate candidate %d", ErrInvalidFoodCategoryReview, decision.ID)
		}
		seenCandidates[decision.ID] = true

		var reviewedFoodTypeID any
		var publishFoodTypeID int64
		switch decision.Decision {
		case "approved":
			publishFoodTypeID = suggestedFoodTypeID
			reviewedFoodTypeID = suggestedFoodTypeID
		case "changed":
			if decision.FoodTypeID <= 0 || decision.FoodTypeID == suggestedFoodTypeID {
				return fmt.Errorf("%w: changed candidate %d needs a different category", ErrInvalidFoodCategoryReview, decision.ID)
			}
			exists, err := activeFoodTypeExists(tx, decision.FoodTypeID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: category %d is unavailable", ErrInvalidFoodCategoryReview, decision.FoodTypeID)
			}
			publishFoodTypeID = decision.FoodTypeID
			reviewedFoodTypeID = decision.FoodTypeID
		case "rejected":
			// Rejections remain in review history and are never published.
		default:
			return fmt.Errorf("%w: unsupported decision %q", ErrInvalidFoodCategoryReview, decision.Decision)
		}

		if _, err := tx.Exec(`
			UPDATE food_category_review_candidates
			SET decision = $1, reviewed_food_type_id = $2, reviewed_at = NOW()
			WHERE id = $3 AND review_task_id = $4
		`, decision.Decision, reviewedFoodTypeID, decision.ID, taskID); err != nil {
			return err
		}
		if publishFoodTypeID > 0 {
			if err := registerPublishedFoodType(tx, publishedFoodTypes, publishFoodTypeID); err != nil {
				return err
			}
			if _, err := tx.Exec(`
				INSERT INTO restaurant_food_types
				(restaurant_id, food_type_id, review_candidate_id, provenance)
				VALUES ($1, $2, $3, 'model_reviewed')
				ON CONFLICT (restaurant_id, food_type_id) DO UPDATE SET
					review_candidate_id = EXCLUDED.review_candidate_id,
					provenance = EXCLUDED.provenance
			`, restaurantID, publishFoodTypeID, decision.ID); err != nil {
				return err
			}
		}
	}

	seenAdditions := map[int64]bool{}
	for _, addition := range submission.Additions {
		if addition.FoodTypeID <= 0 || seenAdditions[addition.FoodTypeID] {
			return fmt.Errorf("%w: invalid or duplicate added category", ErrInvalidFoodCategoryReview)
		}
		seenAdditions[addition.FoodTypeID] = true
		exists, err := activeFoodTypeExists(tx, addition.FoodTypeID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: category %d is unavailable", ErrInvalidFoodCategoryReview, addition.FoodTypeID)
		}
		if err := registerPublishedFoodType(tx, publishedFoodTypes, addition.FoodTypeID); err != nil {
			return err
		}
		var candidateID int64
		if err := tx.QueryRow(`
			INSERT INTO food_category_review_candidates
			(review_task_id, reviewed_food_type_id, decision, origin, reviewed_at)
			VALUES ($1, $2, 'added', 'reviewer', NOW())
			RETURNING id
		`, taskID, addition.FoodTypeID).Scan(&candidateID); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO restaurant_food_types
			(restaurant_id, food_type_id, review_candidate_id, provenance)
			VALUES ($1, $2, $3, 'reviewer_added')
			ON CONFLICT (restaurant_id, food_type_id) DO NOTHING
		`, restaurantID, addition.FoodTypeID, candidateID); err != nil {
			return err
		}
	}

	for _, proposal := range submission.Proposals {
		name := strings.TrimSpace(proposal.ProposedName)
		if name == "" {
			return fmt.Errorf("%w: proposed category needs a name", ErrInvalidFoodCategoryReview)
		}
		var parentFoodTypeID any
		if proposal.ParentFoodTypeID > 0 {
			exists, err := activeFoodTypeExists(tx, proposal.ParentFoodTypeID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: proposed parent category %d is unavailable", ErrInvalidFoodCategoryReview, proposal.ParentFoodTypeID)
			}
			parentFoodTypeID = proposal.ParentFoodTypeID
		}
		evidence, err := json.Marshal(proposal.Evidence)
		if err != nil {
			return fmt.Errorf("%w: invalid proposal evidence", ErrInvalidFoodCategoryReview)
		}
		if _, err := tx.Exec(`
			INSERT INTO food_taxonomy_proposals
			(review_task_id, restaurant_id, proposed_name, parent_food_type_id, explanation, evidence)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, taskID, restaurantID, name, parentFoodTypeID, strings.TrimSpace(proposal.Explanation), evidence); err != nil {
			return err
		}
	}

	var note any
	if value := strings.TrimSpace(submission.Note); value != "" {
		note = value
	}
	if _, err := tx.Exec(`
		UPDATE food_category_review_tasks
		SET status = 'completed', reviewer_note = $1, reviewed_at = NOW()
		WHERE id = $2
	`, note, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func SearchRestaurantsByFoodType(db *sql.DB, query string, limit int) ([]models.FoodTypeSearchResult, error) {
	rows, err := db.Query(`
		WITH RECURSIVE ranked_food_types AS (
			SELECT f.id, f.slug, f.name_en, f.name_ja, f.name_zh, f.aliases, f.description,
			       GREATEST(
				 CASE WHEN lower(f.slug) = lower($1) THEN 2.0 ELSE similarity(f.slug, $1) END,
				 CASE WHEN lower(f.name_en) = lower($1) THEN 2.0 ELSE similarity(f.name_en, $1) END,
				 CASE WHEN f.name_ja = $1 THEN 2.0 ELSE similarity(f.name_ja, $1) END,
				 CASE WHEN f.name_zh = $1 THEN 2.0 ELSE similarity(f.name_zh, $1) END,
				 COALESCE((
					 SELECT MAX(CASE WHEN lower(alias) = lower($1) THEN 2.0 ELSE similarity(alias, $1) END)
					 FROM jsonb_array_elements_text(f.aliases) alias
				 ), 0)
			   ) AS score
			FROM food_types f
			WHERE f.active = TRUE
		), matched_food_type AS (
			SELECT * FROM ranked_food_types
			WHERE score >= 0.25
			ORDER BY score DESC, name_en
			LIMIT 1
		), descendant_food_types AS (
			SELECT f.id, 0 AS depth
			FROM food_types f
			JOIN matched_food_type matched ON matched.id = f.id
			UNION ALL
			SELECT child.id, descendant.depth + 1
			FROM food_types child
			JOIN descendant_food_types descendant ON child.parent_id = descendant.id
			WHERE child.active = TRUE
		), ranked_restaurants AS (
			SELECT link.restaurant_id, descendant.id AS food_type_id,
			       ROW_NUMBER() OVER (
				   PARTITION BY link.restaurant_id
				   ORDER BY descendant.depth DESC, descendant.id
			       ) AS restaurant_rank
			FROM descendant_food_types descendant
			JOIN restaurant_food_types link ON link.food_type_id = descendant.id
		)
		SELECT r.id, COALESCE(r.name_ja, r.name, ''), COALESCE(r.address_ja, r.address, ''),
		       COALESCE(r.url, ''), COALESCE(r.rating, 0), COALESCE(r.latitude, 0), COALESCE(r.longitude, 0),
		       f.id, f.slug, f.name_en, f.name_ja, f.name_zh, f.aliases, f.description,
		       COALESCE(f.parent_id, 0), COALESCE(parent.slug, ''),
		       COALESCE(parent.name_en, ''), COALESCE(parent.name_ja, ''),
		       COALESCE(parent.name_zh, '')
		FROM ranked_restaurants matched_restaurant
		JOIN restaurants r ON r.id = matched_restaurant.restaurant_id
		JOIN food_types f ON f.id = matched_restaurant.food_type_id
		LEFT JOIN food_types parent ON parent.id = f.parent_id
		WHERE matched_restaurant.restaurant_rank = 1
		ORDER BY r.rating DESC NULLS LAST, r.id
		LIMIT $2
	`, strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []models.FoodTypeSearchResult{}
	for rows.Next() {
		var result models.FoodTypeSearchResult
		var aliasesJSON []byte
		if err := rows.Scan(
			&result.RestaurantID,
			&result.RestaurantName,
			&result.RestaurantAddress,
			&result.RestaurantURL,
			&result.RestaurantRating,
			&result.Latitude,
			&result.Longitude,
			&result.FoodType.ID,
			&result.FoodType.Slug,
			&result.FoodType.NameEN,
			&result.FoodType.NameJA,
			&result.FoodType.NameZH,
			&aliasesJSON,
			&result.FoodType.Description,
			&result.FoodType.ParentID,
			&result.FoodType.ParentSlug,
			&result.FoodType.ParentNameEN,
			&result.FoodType.ParentNameJA,
			&result.FoodType.ParentNameZH,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(aliasesJSON, &result.FoodType.Aliases); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}
