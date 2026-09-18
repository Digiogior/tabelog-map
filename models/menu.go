package models

type MenuItem struct {
	RestaurantID int64
	Name         string
	Price        *int
	PriceBottle  *int
	Description  *string
}

type MenuSearchResult struct {
	RestaurantName    string  `json:"restaurant_name"`
	RestaurantAddress string  `json:"restaurant_address"`
	RestaurantURL     string  `json:"restaurant_url"`
	RestaurantRating  float64 `json:"restaurant_rating"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	ItemName          string  `json:"item_name"`
	Price             *int    `json:"price"`
	Description       *string `json:"description"`
}

type MenuReviewItem struct {
	ID               int64    `json:"id"`
	ExtractedName    string   `json:"extracted_name"`
	ReviewedName     *string  `json:"reviewed_name,omitempty"`
	Decision         string   `json:"decision"`
	EvidenceBlockIDs []string `json:"evidence_block_ids"`
}

type MenuReviewTask struct {
	ID             int64            `json:"id"`
	RestaurantID   int64            `json:"restaurant_id"`
	RestaurantName string           `json:"restaurant_name"`
	RestaurantURL  string           `json:"restaurant_url"`
	ImageURL       string           `json:"image_url"`
	SourcePageURL  string           `json:"source_page_url"`
	Items          []MenuReviewItem `json:"items"`
}

type MenuReviewDecision struct {
	ID           int64  `json:"id"`
	Decision     string `json:"decision"`
	ReviewedName string `json:"reviewed_name"`
}

type MenuReviewSubmission struct {
	Items []MenuReviewDecision `json:"items"`
	Note  string               `json:"note"`
}

type MenuReviewStats struct {
	PendingTasks   int `json:"pending_tasks"`
	CompletedTasks int `json:"completed_tasks"`
	PendingItems   int `json:"pending_items"`
}

type FoodType struct {
	ID           int64    `json:"id"`
	Slug         string   `json:"slug"`
	NameEN       string   `json:"name_en"`
	NameJA       string   `json:"name_ja"`
	NameZH       string   `json:"name_zh"`
	Aliases      []string `json:"aliases"`
	Description  string   `json:"description"`
	ParentID     int64    `json:"parent_id,omitempty"`
	ParentSlug   string   `json:"parent_slug,omitempty"`
	ParentNameEN string   `json:"parent_name_en,omitempty"`
	ParentNameJA string   `json:"parent_name_ja,omitempty"`
	ParentNameZH string   `json:"parent_name_zh,omitempty"`
}

type FoodCategoryEvidence struct {
	ID            int64    `json:"id,omitempty"`
	ImageURL      string   `json:"image_url"`
	SourcePageURL string   `json:"source_page_url"`
	Text          string   `json:"text"`
	BlockIDs      []string `json:"block_ids"`
}

type FoodCategoryReviewCandidate struct {
	ID                int64                  `json:"id"`
	SuggestedFoodType *FoodType              `json:"suggested_food_type,omitempty"`
	ReviewedFoodType  *FoodType              `json:"reviewed_food_type,omitempty"`
	Decision          string                 `json:"decision"`
	Origin            string                 `json:"origin"`
	Evidence          []FoodCategoryEvidence `json:"evidence"`
}

type FoodCategoryReviewTask struct {
	ID             int64                         `json:"id"`
	RestaurantID   int64                         `json:"restaurant_id"`
	RestaurantName string                        `json:"restaurant_name"`
	RestaurantURL  string                        `json:"restaurant_url"`
	Photos         []string                      `json:"photos"`
	Candidates     []FoodCategoryReviewCandidate `json:"candidates"`
}

type FoodCategoryReviewDecision struct {
	ID         int64  `json:"id"`
	Decision   string `json:"decision"`
	FoodTypeID int64  `json:"food_type_id,omitempty"`
}

type FoodCategoryReviewAddition struct {
	FoodTypeID int64 `json:"food_type_id"`
}

type TaxonomyProposalSubmission struct {
	ProposedName     string                 `json:"proposed_name"`
	ParentFoodTypeID int64                  `json:"parent_food_type_id,omitempty"`
	Explanation      string                 `json:"explanation"`
	Evidence         []FoodCategoryEvidence `json:"evidence,omitempty"`
}

type FoodCategoryReviewSubmission struct {
	Candidates []FoodCategoryReviewDecision `json:"candidates"`
	Additions  []FoodCategoryReviewAddition `json:"additions"`
	Proposals  []TaxonomyProposalSubmission `json:"proposals"`
	Note       string                       `json:"note"`
}

type FoodCategoryReviewStats struct {
	PendingTasks      int `json:"pending_tasks"`
	CompletedTasks    int `json:"completed_tasks"`
	PendingCategories int `json:"pending_categories"`
}

type FoodTypeSearchResult struct {
	RestaurantID      int64    `json:"restaurant_id"`
	RestaurantName    string   `json:"restaurant_name"`
	RestaurantAddress string   `json:"restaurant_address"`
	RestaurantURL     string   `json:"restaurant_url"`
	RestaurantRating  float64  `json:"restaurant_rating"`
	Latitude          float64  `json:"latitude"`
	Longitude         float64  `json:"longitude"`
	FoodType          FoodType `json:"food_type"`
}
