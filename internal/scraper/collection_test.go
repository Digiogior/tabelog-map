package scraper

import (
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func testRetryPolicy(maxRetries int) retryPolicy {
	return retryPolicy{
		maxRetries: maxRetries,
		sleep:      func(time.Duration) {},
	}
}

func TestRequestRetrierRecoversTransientFailure(t *testing.T) {
	retrier := newRequestRetrier(testRetryPolicy(3), nil)
	requestURL := "https://example.test/list"
	requests := 1
	var retry func() error
	retry = func() error {
		requests++
		if requests < 3 {
			retrier.handleError(requestURL, 503, nil, errors.New("try again"), retry)
		} else {
			retrier.handleSuccess(requestURL)
		}
		return nil
	}
	retrier.handleError(requestURL, 503, nil, errors.New("try again"), retry)

	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
	if err := retrier.Err(); err != nil {
		t.Fatalf("retrier reported a recovered request as failed: %v", err)
	}
}

func TestRetryDelayHonorsRetryAfterBeyondLocalCap(t *testing.T) {
	headers := http.Header{"Retry-After": []string{"120"}}
	policy := retryPolicy{
		baseDelay: 5 * time.Second,
		maxDelay:  time.Minute,
	}
	if got, want := retryDelay(policy, 4, &headers, time.Now()), 2*time.Minute; got != want {
		t.Fatalf("retry delay = %s, want %s", got, want)
	}
}

func TestFailedPathStorePersistsAndResolvesExhaustedRequest(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "urls.failed-paths.csv")
	store, err := openFailedPathStore(journal)
	if err != nil {
		t.Fatal(err)
	}

	firstRetrier := newRequestRetrier(testRetryPolicy(2), store)
	requestURL := "https://example.test/list/49/"
	requests := 1
	var retry func() error
	retry = func() error {
		requests++
		firstRetrier.handleError(requestURL, 503, nil, errors.New("still unavailable"), retry)
		return nil
	}
	firstRetrier.handleError(requestURL, 503, nil, errors.New("still unavailable"), retry)

	if requests != 3 {
		t.Fatalf("requests before exhaustion = %d, want 3", requests)
	}
	if got := store.Snapshot(); !reflect.DeepEqual(got, []string{requestURL}) {
		t.Fatalf("persisted paths = %#v, want %#v", got, []string{requestURL})
	}
	if err := store.Compact(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openFailedPathStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot(); !reflect.DeepEqual(got, []string{requestURL}) {
		t.Fatalf("reopened paths = %#v, want %#v", got, []string{requestURL})
	}

	secondRetrier := newRequestRetrier(testRetryPolicy(2), reopened)
	secondRetrier.handleSuccess(requestURL)
	if got := reopened.Snapshot(); len(got) != 0 {
		t.Fatalf("resolved request remains in journal: %#v", got)
	}
}

func TestFallbackPathsForCapsBuildsSubAreaCuisinePartitions(t *testing.T) {
	discoveries := []areaDiscovery{{
		area:     "hyogo/A2801/",
		subAreas: []string{"hyogo/A2801/", "hyogo/A2801/A280101/", "hyogo/A2801/A280102/"},
		categoryPaths: []string{
			"hyogo/A2801/rstLst/washoku/?SrtT=rt",
			"hyogo/A2801/rstLst/cafe/?SrtT=rt",
		},
	}}
	outcomes := []crawlOutcome{
		{
			target:     "https://tabelog.com/tw/hyogo/A2801/A280101/rstLst/60/",
			hitPageCap: true,
		},
		{
			target:     "https://tabelog.com/tw/hyogo/A2801/rstLst/washoku/60/?SrtT=rt",
			hitPageCap: true,
		},
		{
			// A capped top-level total is already partitioned by the initial
			// sub-area paths, so it does not add the full cross-product itself.
			target:     "https://tabelog.com/tw/hyogo/A2801/rstLst/60/",
			hitPageCap: true,
		},
	}

	got := fallbackPathsForCaps(outcomes, discoveries)
	sort.Strings(got)
	want := []string{
		"hyogo/A2801/A280101/rstLst/cafe/?SrtT=rt",
		"hyogo/A2801/A280101/rstLst/washoku/?SrtT=rt",
		"hyogo/A2801/A280102/rstLst/washoku/?SrtT=rt",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback paths = %#v, want %#v", got, want)
	}
}

func TestPathsWithoutResumedPartitionsSkipsOnlyMatchingPartition(t *testing.T) {
	paths := []string{
		"hyogo/A2801/rstLst/washoku/?SrtT=rt",
		"hyogo/A2801/rstLst/cafe/?SrtT=rt",
		"hyogo/A2801/A280101/",
	}
	resumePaths := []string{
		"https://tabelog.com/tw/hyogo/A2801/rstLst/washoku/49/?SrtT=rt",
	}
	want := []string{
		"hyogo/A2801/rstLst/cafe/?SrtT=rt",
		"hyogo/A2801/A280101/",
	}
	if got := pathsWithoutResumedPartitions(paths, resumePaths); !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered paths = %#v, want %#v", got, want)
	}
}

func TestParseListingPartitionIncludesStation(t *testing.T) {
	got := parseListingPartition(
		"https://tabelog.com/tw/toyama/A1601/A160101/R13773/rstLst/RC/4/?SrtT=rt",
	)
	want := listingPartition{
		topArea:  "toyama/A1601/",
		subArea:  "toyama/A1601/A160101/",
		station:  "toyama/A1601/A160101/R13773/",
		category: "RC",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partition = %#v, want %#v", got, want)
	}
}

func TestStationPathForPartitionRetainsSubAreaAndCuisine(t *testing.T) {
	parent := parseListingPartition(
		"https://tabelog.com/tw/toyama/A1601/A160101/rstLst/RC/?SrtT=rt",
	)
	got, ok := stationPathForPartition(
		"/tw/toyama/A1601/A160101/R13773/rstLst/RC/?SrtT=rt",
		parent,
	)
	if !ok {
		t.Fatal("matching station path was rejected")
	}
	want := "https://tabelog.com/tw/toyama/A1601/A160101/R13773/rstLst/RC/?SrtT=rt"
	if got != want {
		t.Fatalf("station path = %q, want %q", got, want)
	}

	if _, ok := stationPathForPartition(
		"/tw/toyama/A1601/A160101/R13773/rstLst/washoku/?SrtT=rt",
		parent,
	); ok {
		t.Fatal("station path for a different cuisine was accepted")
	}
	if _, ok := stationPathForPartition(
		"/tw/toyama/A1604/A160401/R11823/rstLst/RC/?SrtT=rt",
		parent,
	); ok {
		t.Fatal("station path for a different sub-area was accepted")
	}
}

func TestStationCategoryPathForPartitionRetainsStationAndChangesCuisine(t *testing.T) {
	parent := parseListingPartition(
		"https://tabelog.com/tw/kagoshima/A4601/A460101/R2358/rstLst/RC/?SrtT=rt",
	)
	got, ok := stationCategoryPathForPartition(
		"/tw/kagoshima/A4601/A460101/R2358/rstLst/RC02/?SrtT=rt",
		parent,
	)
	if !ok {
		t.Fatal("matching station cuisine path was rejected")
	}
	want := "https://tabelog.com/tw/kagoshima/A4601/A460101/R2358/rstLst/RC02/?SrtT=rt"
	if got != want {
		t.Fatalf("station cuisine path = %q, want %q", got, want)
	}

	if _, ok := stationCategoryPathForPartition(
		"/tw/kagoshima/A4601/A460101/R2358/rstLst/RC/?SrtT=rt",
		parent,
	); ok {
		t.Fatal("unchanged station cuisine path was accepted")
	}
	if _, ok := stationCategoryPathForPartition(
		"/tw/kagoshima/A4601/A460101/R850/rstLst/RC02/?SrtT=rt",
		parent,
	); ok {
		t.Fatal("cuisine path for a different station was accepted")
	}
}

func TestListingPageNumber(t *testing.T) {
	tests := map[string]int{
		"hyogo/A2804/": 1,
		"https://tabelog.com/tw/hyogo/A2804/rstLst/60/":                 60,
		"https://tabelog.com/tw/hyogo/A2804/rstLst/washoku/60/?SrtT=rt": 60,
	}
	for rawURL, want := range tests {
		if got := listingPageNumber(rawURL); got != want {
			t.Errorf("listingPageNumber(%q) = %d, want %d", rawURL, got, want)
		}
	}
}
