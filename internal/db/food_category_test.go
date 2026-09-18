package db

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedFoodTaxonomyHasValidHierarchy(t *testing.T) {
	var seeds []foodTypeSeed
	if err := json.Unmarshal(foodTaxonomyJSON, &seeds); err != nil {
		t.Fatalf("decode embedded taxonomy: %v", err)
	}
	if err := validateFoodTypeSeeds(seeds); err != nil {
		t.Fatalf("validate embedded taxonomy: %v", err)
	}
	if len(seeds) < 70 {
		t.Fatalf("expected detailed taxonomy, got %d food types", len(seeds))
	}

	parents := make(map[string]string, len(seeds))
	for _, seed := range seeds {
		parents[seed.Slug] = seed.ParentSlug
	}
	for _, slug := range []string{
		"japanese_sweets", "cake", "pudding", "parfait", "ice_cream",
		"crepe_pancake", "baked_sweets", "fruit",
	} {
		if parents[slug] != "dessert" {
			t.Errorf("expected %s to be a dessert child, got parent %q", slug, parents[slug])
		}
	}
}

func TestFoodTaxonomyValidationRejectsCycles(t *testing.T) {
	seeds := []foodTypeSeed{
		{Slug: "one", ParentSlug: "two"},
		{Slug: "two", ParentSlug: "one"},
	}
	if err := validateFoodTypeSeeds(seeds); err == nil {
		t.Fatal("expected a parent cycle to be rejected")
	}
}
