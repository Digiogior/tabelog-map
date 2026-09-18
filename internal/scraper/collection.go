package scraper

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly"
)

const (
	discoverWorkers          = 3
	crawlWorkers             = 3
	collectionMaxRetries     = 4
	collectionRequestTimeout = 30 * time.Second
	collectionRetryBaseDelay = 5 * time.Second
	collectionRetryMaxDelay  = time.Minute
	listingPageCap           = 60
)

type retryPolicy struct {
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
	sleep      func(time.Duration)
}

var defaultCollectionRetryPolicy = retryPolicy{
	maxRetries: collectionMaxRetries,
	baseDelay:  collectionRetryBaseDelay,
	maxDelay:   collectionRetryMaxDelay,
	sleep:      time.Sleep,
}

// failedPathStore is an append-only journal while a collection is running.
// Persisting every transition immediately means an interrupted process can
// resume exhausted listing pages on the next run. Compact rewrites the journal
// to its current unresolved set when the run finishes.
type failedPathStore struct {
	mu       sync.Mutex
	filename string
	failed   map[string]struct{}
}

func failedPathsFilename(urlsFilename string) string {
	ext := filepath.Ext(urlsFilename)
	base := strings.TrimSuffix(urlsFilename, ext)
	return base + ".failed-paths.csv"
}

func openFailedPathStore(filename string) (*failedPathStore, error) {
	store := &failedPathStore{
		filename: filename,
		failed:   make(map[string]struct{}),
	}

	file, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read failed-path journal %s: %w", filename, err)
	}
	for _, row := range records {
		if len(row) == 0 {
			continue
		}
		if len(row) == 1 {
			requestURL := strings.TrimSpace(row[0])
			if requestURL != "" && requestURL != "url" {
				store.failed[requestURL] = struct{}{}
			}
			continue
		}

		state := strings.TrimSpace(row[0])
		requestURL := strings.TrimSpace(row[1])
		if state == "state" || requestURL == "" {
			continue
		}
		switch state {
		case "failed":
			store.failed[requestURL] = struct{}{}
		case "resolved":
			delete(store.failed, requestURL)
		}
	}
	return store, nil
}

func (s *failedPathStore) appendEventLocked(state, requestURL string) error {
	file, err := os.OpenFile(s.filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return statErr
	}
	writer := csv.NewWriter(file)
	if info.Size() == 0 {
		if err := writer.Write([]string{"state", "url"}); err != nil {
			file.Close()
			return err
		}
	}
	if err := writer.Write([]string{state, requestURL}); err != nil {
		file.Close()
		return err
	}
	writer.Flush()
	writeErr := writer.Error()
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func (s *failedPathStore) MarkFailed(requestURL string) error {
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.failed[requestURL]; exists {
		return nil
	}
	if err := s.appendEventLocked("failed", requestURL); err != nil {
		return err
	}
	s.failed[requestURL] = struct{}{}
	return nil
}

func (s *failedPathStore) MarkResolved(requestURL string) error {
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.failed[requestURL]; !exists {
		return nil
	}
	if err := s.appendEventLocked("resolved", requestURL); err != nil {
		return err
	}
	delete(s.failed, requestURL)
	return nil
}

func (s *failedPathStore) Snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths := make([]string, 0, len(s.failed))
	for path := range s.failed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (s *failedPathStore) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.filename)
	temp, err := os.CreateTemp(dir, filepath.Base(s.filename)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempName)
	}

	writer := csv.NewWriter(temp)
	if err := writer.Write([]string{"state", "url"}); err != nil {
		cleanup()
		return err
	}
	paths := make([]string, 0, len(s.failed))
	for path := range s.failed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := writer.Write([]string{"failed", path}); err != nil {
			cleanup()
			return err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Chmod(0644); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, s.filename); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

type requestRetrier struct {
	mu          sync.Mutex
	policy      retryPolicy
	store       *failedPathStore
	attempts    map[string]int
	failures    map[string]error
	persistErrs []error
	succeeded   bool
}

func newRequestRetrier(policy retryPolicy, store *failedPathStore) *requestRetrier {
	if policy.sleep == nil {
		policy.sleep = time.Sleep
	}
	return &requestRetrier{
		policy:   policy,
		store:    store,
		attempts: make(map[string]int),
		failures: make(map[string]error),
	}
}

func shouldRetryRequest(statusCode int) bool {
	return statusCode == 0 ||
		statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusTooEarly ||
		statusCode == http.StatusTooManyRequests ||
		statusCode >= http.StatusInternalServerError
}

func retryAfter(headers *http.Header, now time.Time) time.Duration {
	if headers == nil {
		return 0
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		if delay := retryAt.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

func retryDelay(policy retryPolicy, retryNumber int, headers *http.Header, now time.Time) time.Duration {
	delay := policy.baseDelay
	if retryNumber > 1 {
		delay *= time.Duration(1 << (retryNumber - 1))
	}
	if policy.maxDelay > 0 && delay > policy.maxDelay {
		delay = policy.maxDelay
	}
	// Retry-After is an explicit server instruction and must not be shortened
	// by the local exponential-backoff cap.
	if fromHeader := retryAfter(headers, now); fromHeader > delay {
		delay = fromHeader
	}
	return delay
}

func (r *requestRetrier) markExhausted(requestURL string, requestErr error) {
	r.mu.Lock()
	r.failures[requestURL] = requestErr
	delete(r.attempts, requestURL)
	r.mu.Unlock()

	if r.store != nil {
		if err := r.store.MarkFailed(requestURL); err != nil {
			r.mu.Lock()
			r.persistErrs = append(r.persistErrs, fmt.Errorf("persist failed path %s: %w", requestURL, err))
			r.mu.Unlock()
		}
	}
}

func (r *requestRetrier) handleSuccess(requestURL string) {
	r.mu.Lock()
	r.succeeded = true
	delete(r.attempts, requestURL)
	delete(r.failures, requestURL)
	r.mu.Unlock()

	if r.store != nil {
		if err := r.store.MarkResolved(requestURL); err != nil {
			r.mu.Lock()
			r.persistErrs = append(r.persistErrs, fmt.Errorf("resolve failed path %s: %w", requestURL, err))
			r.mu.Unlock()
		}
	}
}

func (r *requestRetrier) handleError(
	requestURL string,
	statusCode int,
	headers *http.Header,
	requestErr error,
	retry func() error,
) {
	r.mu.Lock()
	retriesUsed := r.attempts[requestURL]
	canRetry := shouldRetryRequest(statusCode) && retriesUsed < r.policy.maxRetries
	if canRetry {
		retriesUsed++
		r.attempts[requestURL] = retriesUsed
	}
	r.mu.Unlock()

	if !canRetry {
		fmt.Printf("Giving up: %s (status %d): %v\n", requestURL, statusCode, requestErr)
		r.markExhausted(requestURL, requestErr)
		return
	}

	delay := retryDelay(r.policy, retriesUsed, headers, time.Now())
	fmt.Printf(
		"Retrying: %s (status %d, retry %d/%d, backoff %s): %v\n",
		requestURL,
		statusCode,
		retriesUsed,
		r.policy.maxRetries,
		delay.Round(time.Second),
		requestErr,
	)
	r.policy.sleep(delay)
	_ = retry()
}

func (r *requestRetrier) Attach(c *colly.Collector) {
	c.OnResponse(func(response *colly.Response) {
		r.handleSuccess(response.Request.URL.String())
	})

	c.OnError(func(response *colly.Response, requestErr error) {
		r.handleError(
			response.Request.URL.String(),
			response.StatusCode,
			response.Headers,
			requestErr,
			response.Request.Retry,
		)
	})
}

func (r *requestRetrier) FailureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.failures)
}

func (r *requestRetrier) Succeeded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.succeeded
}

func (r *requestRetrier) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	errs := make([]error, 0, len(r.failures)+len(r.persistErrs))
	paths := make([]string, 0, len(r.failures))
	for path := range r.failures {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		errs = append(errs, fmt.Errorf("%s: %w", path, r.failures[path]))
	}
	errs = append(errs, r.persistErrs...)
	return errors.Join(errs...)
}

func newCollectionCollector(delay time.Duration, allowedDomains ...string) *colly.Collector {
	if len(allowedDomains) == 0 {
		allowedDomains = []string{"tabelog.com"}
	}
	c := colly.NewCollector(colly.AllowedDomains(allowedDomains...))
	c.Limit(&colly.LimitRule{DomainGlob: "*", Delay: delay, Parallelism: 1})
	c.SetRequestTimeout(collectionRequestTimeout)
	c.OnRequest(func(request *colly.Request) {
		request.Headers.Set("User-Agent", "MyResearchBot/1.0")
	})
	return c
}

func visitWithRetries(c *colly.Collector, targetURL string, retrier *requestRetrier) error {
	visitErr := c.Visit(targetURL)
	if err := retrier.Err(); err != nil {
		return err
	}
	if visitErr != nil && !retrier.Succeeded() {
		return visitErr
	}
	return nil
}

func segCount(path string) int {
	return len(strings.FieldsFunc(strings.TrimSuffix(path, "/"), func(r rune) bool { return r == '/' }))
}

// fetchSubAreaPaths returns the area itself and its immediate child areas.
func fetchSubAreaPaths(city string) ([]string, error) {
	targetURL := "https://tabelog.com/tw/" + city
	subAreas := []string{city}
	seen := map[string]bool{city: true}
	citySegs := segCount(city)

	c := newCollectionCollector(3 * time.Second)
	retrier := newRequestRetrier(defaultCollectionRetryPolicy, nil)
	retrier.Attach(c)

	c.OnHTML("a[href]", func(element *colly.HTMLElement) {
		href := element.Attr("href")
		href = strings.TrimPrefix(href, "https://tabelog.com")
		href = strings.TrimPrefix(href, "/tw/")
		if !strings.HasPrefix(href, city) || href == city {
			return
		}
		path := strings.TrimSuffix(href, "/") + "/"
		segments := strings.FieldsFunc(strings.TrimSuffix(path, "/"), func(r rune) bool { return r == '/' })
		if len(segments) == citySegs+1 &&
			strings.HasPrefix(segments[len(segments)-1], "A") &&
			!seen[path] {
			seen[path] = true
			subAreas = append(subAreas, path)
			fmt.Println("Found sub-area:", path)
		}
	})

	if err := visitWithRetries(c, targetURL, retrier); err != nil {
		return nil, fmt.Errorf("discover sub-areas for %s: %w", city, err)
	}
	return subAreas, nil
}

// fetchAreaPaths returns the top-level Tabelog areas for a prefecture.
func fetchAreaPaths(prefecture string) ([]string, error) {
	targetURL := "https://tabelog.com/tw/" + prefecture + "/"
	var areas []string
	seen := map[string]bool{}

	c := newCollectionCollector(3 * time.Second)
	retrier := newRequestRetrier(defaultCollectionRetryPolicy, nil)
	retrier.Attach(c)

	c.OnHTML("#tabs-panel-balloon-pref-area a.c-link-arrow", func(element *colly.HTMLElement) {
		href := element.Attr("href")
		href = strings.TrimPrefix(href, "/tw/")
		if index := strings.Index(href, "/rstLst"); index != -1 {
			href = href[:index+1]
		}
		if href != "" && !seen[href] {
			seen[href] = true
			areas = append(areas, href)
			fmt.Println("Found area:", href)
		}
	})

	if err := visitWithRetries(c, targetURL, retrier); err != nil {
		return nil, fmt.Errorf("discover areas for %s: %w", prefecture, err)
	}
	return areas, nil
}

// fetchCategoryPaths returns cuisine listing paths for a top-level area.
func fetchCategoryPaths(areaPath string) ([]string, error) {
	targetURL := "https://tabelog.com/tw/" + areaPath + "rstLst/?SrtT=rt"
	var paths []string
	seen := map[string]bool{}

	c := newCollectionCollector(3 * time.Second)
	retrier := newRequestRetrier(defaultCollectionRetryPolicy, nil)
	retrier.Attach(c)

	c.OnHTML("#js-leftnavi-genre-scroll a.list-balloon__btn", func(element *colly.HTMLElement) {
		href := element.Attr("href")
		href = strings.TrimPrefix(href, "https://tabelog.com")
		href = strings.TrimPrefix(href, "/tw/")
		if strings.Contains(href, "/rstLst/") && !seen[href] {
			seen[href] = true
			paths = append(paths, href)
		}
	})

	if err := visitWithRetries(c, targetURL, retrier); err != nil {
		return nil, fmt.Errorf("discover categories for %s: %w", areaPath, err)
	}
	return paths, nil
}

type areaDiscovery struct {
	area          string
	subAreas      []string
	categoryPaths []string
	err           error
}

func discoverAreaPartitions(areas []string) ([]areaDiscovery, error) {
	results := make([]areaDiscovery, len(areas))
	var waitGroup sync.WaitGroup
	semaphore := make(chan struct{}, discoverWorkers)

	for index, area := range areas {
		waitGroup.Add(1)
		semaphore <- struct{}{}
		go func(resultIndex int, areaPath string) {
			defer waitGroup.Done()
			defer func() { <-semaphore }()

			fmt.Printf("[%d/%d] Discovering paths for area: %s\n", resultIndex+1, len(areas), areaPath)
			subAreas, subAreaErr := fetchSubAreaPaths(areaPath)
			categoryPaths, categoryErr := fetchCategoryPaths(areaPath)
			results[resultIndex] = areaDiscovery{
				area:          areaPath,
				subAreas:      subAreas,
				categoryPaths: categoryPaths,
				err:           errors.Join(subAreaErr, categoryErr),
			}
		}(index, area)
	}
	waitGroup.Wait()

	var errs []error
	for _, result := range results {
		if result.err != nil {
			errs = append(errs, result.err)
		}
	}
	return results, errors.Join(errs...)
}

func ensureURLFile(filename string) (map[string]bool, error) {
	collected := make(map[string]bool)
	file, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		writer := csv.NewWriter(file)
		writeErr := writer.Write([]string{"url"})
		writer.Flush()
		err = errors.Join(writeErr, writer.Error(), file.Close())
		return collected, err
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	for _, row := range records {
		if len(row) == 0 {
			continue
		}
		requestURL := strings.TrimSpace(row[0])
		if requestURL == "" || requestURL == "url" || requestURL == "urls" {
			continue
		}
		collected[requestURL] = true
	}
	return collected, nil
}

func appendRestaurantURL(
	filename string,
	restaurantURL string,
	collected map[string]bool,
	mu *sync.Mutex,
) error {
	restaurantURL = strings.TrimSpace(restaurantURL)
	if restaurantURL == "" {
		return nil
	}

	mu.Lock()
	defer mu.Unlock()
	if collected[restaurantURL] {
		return nil
	}

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(file)
	writeErr := writer.Write([]string{restaurantURL})
	writer.Flush()
	err = errors.Join(writeErr, writer.Error(), file.Close())
	if err != nil {
		return err
	}
	collected[restaurantURL] = true
	return nil
}

func absoluteListingURL(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		return path
	}
	path = strings.TrimPrefix(path, "/tw/")
	path = strings.TrimPrefix(path, "/")
	return "https://tabelog.com/tw/" + path
}

func listingPageNumber(rawURL string) int {
	parsed, err := url.Parse(absoluteListingURL(rawURL))
	if err != nil {
		return 0
	}
	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	for index, segment := range segments {
		if segment != "rstLst" {
			continue
		}
		for _, candidate := range segments[index+1:] {
			if page, err := strconv.Atoi(candidate); err == nil {
				return page
			}
		}
	}
	return 1
}

type crawlOutcome struct {
	target        string
	hitPageCap    bool
	failedPages   int
	collectionErr error
}

func fetchUrlsFromFullURL(
	targetURL string,
	destFile string,
	collected map[string]bool,
	mu *sync.Mutex,
	failedStore *failedPathStore,
) crawlOutcome {
	targetURL = absoluteListingURL(targetURL)
	outcome := crawlOutcome{target: targetURL}
	visitedPages := map[string]bool{targetURL: true}
	var callbackErr error

	c := newCollectionCollector(5 * time.Second)
	retrier := newRequestRetrier(defaultCollectionRetryPolicy, failedStore)
	retrier.Attach(c)

	c.OnResponse(func(response *colly.Response) {
		if listingPageNumber(response.Request.URL.String()) >= listingPageCap {
			outcome.hitPageCap = true
		}
	})

	c.OnHTML("a.list-rst__rst-name-target.cpy-rst-name", func(element *colly.HTMLElement) {
		if err := appendRestaurantURL(destFile, element.Attr("href"), collected, mu); err != nil {
			callbackErr = errors.Join(callbackErr, err)
		}
	})

	c.OnHTML("a.c-pagination__arrow--next", func(element *colly.HTMLElement) {
		nextPage := element.Request.AbsoluteURL(element.Attr("href"))
		if nextPage == "" || visitedPages[nextPage] {
			return
		}
		visitedPages[nextPage] = true
		fmt.Println("scraping:", nextPage)
		// The retrier owns request errors. Colly returns the original error even
		// when an OnError retry succeeds, so treating this return value as final
		// would incorrectly fail a recovered crawl.
		_ = element.Request.Visit(nextPage)
	})

	visitErr := c.Visit(targetURL)
	if visitErr != nil && !retrier.Succeeded() && retrier.FailureCount() == 0 {
		retrier.markExhausted(targetURL, visitErr)
	}
	outcome.failedPages = retrier.FailureCount()
	outcome.collectionErr = errors.Join(callbackErr, retrier.Err())
	return outcome
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func partitionKey(rawURL string) string {
	partition := parseListingPartition(rawURL)
	if partition.topArea == "" {
		return absoluteListingURL(rawURL)
	}
	return partition.topArea + "|" + partition.subArea + "|" + partition.station + "|" + partition.category
}

func pathsWithoutResumedPartitions(paths, resumePaths []string) []string {
	resumed := make(map[string]bool, len(resumePaths))
	for _, path := range resumePaths {
		resumed[partitionKey(path)] = true
	}

	var filtered []string
	for _, path := range paths {
		if !resumed[partitionKey(path)] {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

func crawlBatch(
	paths []string,
	filename string,
	collected map[string]bool,
	mu *sync.Mutex,
	failedStore *failedPathStore,
) []crawlOutcome {
	paths = dedupeStrings(paths)
	results := make([]crawlOutcome, len(paths))
	var waitGroup sync.WaitGroup
	semaphore := make(chan struct{}, crawlWorkers)

	for index, path := range paths {
		waitGroup.Add(1)
		semaphore <- struct{}{}
		go func(resultIndex int, listingPath string) {
			defer waitGroup.Done()
			defer func() { <-semaphore }()

			fmt.Printf("[%d/%d] Crawling: %s\n", resultIndex+1, len(paths), listingPath)
			results[resultIndex] = fetchUrlsFromFullURL(
				listingPath,
				filename,
				collected,
				mu,
				failedStore,
			)
		}(index, path)
	}
	waitGroup.Wait()
	return results
}

type listingPartition struct {
	topArea  string
	subArea  string
	station  string
	category string
}

func parseListingPartition(rawURL string) listingPartition {
	parsed, err := url.Parse(absoluteListingURL(rawURL))
	if err != nil {
		return listingPartition{}
	}
	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	if len(segments) > 0 && segments[0] == "tw" {
		segments = segments[1:]
	}
	if len(segments) < 2 {
		return listingPartition{}
	}

	rstIndex := len(segments)
	for index, segment := range segments {
		if segment == "rstLst" {
			rstIndex = index
			break
		}
	}
	areaSegments := segments[:rstIndex]
	if len(areaSegments) < 2 {
		return listingPartition{}
	}

	info := listingPartition{
		topArea: strings.Join(areaSegments[:2], "/") + "/",
	}
	if len(areaSegments) >= 3 && strings.HasPrefix(areaSegments[2], "A") {
		info.subArea = strings.Join(areaSegments[:3], "/") + "/"
	}
	if len(areaSegments) >= 4 && strings.HasPrefix(areaSegments[3], "R") {
		info.station = strings.Join(areaSegments[:4], "/") + "/"
	}
	if rstIndex < len(segments) {
		var categorySegments []string
		for _, segment := range segments[rstIndex+1:] {
			if _, err := strconv.Atoi(segment); err == nil {
				break
			}
			categorySegments = append(categorySegments, segment)
		}
		info.category = strings.Join(categorySegments, "/")
	}
	return info
}

func categoryFromPath(categoryPath string) string {
	return parseListingPartition(categoryPath).category
}

func categoryListingPath(areaPath, category string) string {
	return strings.TrimSuffix(areaPath, "/") + "/rstLst/" + strings.Trim(category, "/") + "/?SrtT=rt"
}

// fallbackPathsForCaps builds only the finer partitions required by capped
// listings:
//   - capped sub-area total -> that sub-area × all cuisines
//   - capped top-area cuisine -> every child sub-area × that cuisine
func fallbackPathsForCaps(outcomes []crawlOutcome, discoveries []areaDiscovery) []string {
	byArea := make(map[string]areaDiscovery, len(discoveries))
	for _, discovery := range discoveries {
		byArea[discovery.area] = discovery
	}

	var paths []string
	for _, outcome := range outcomes {
		if !outcome.hitPageCap {
			continue
		}
		partition := parseListingPartition(outcome.target)
		discovery, exists := byArea[partition.topArea]
		if !exists {
			continue
		}

		switch {
		case partition.subArea != "" && partition.category == "":
			for _, categoryPath := range discovery.categoryPaths {
				if category := categoryFromPath(categoryPath); category != "" {
					paths = append(paths, categoryListingPath(partition.subArea, category))
				}
			}
		case partition.subArea == "" && partition.category != "":
			for _, subArea := range discovery.subAreas {
				if subArea != discovery.area {
					paths = append(paths, categoryListingPath(subArea, partition.category))
				}
			}
		}
	}
	return dedupeStrings(paths)
}

func stationPathForPartition(rawURL string, parent listingPartition) (string, bool) {
	candidate := parseListingPartition(rawURL)
	if candidate.station == "" ||
		candidate.topArea != parent.topArea ||
		candidate.subArea != parent.subArea ||
		candidate.category != parent.category {
		return "", false
	}
	return absoluteListingURL(rawURL), true
}

func stationCategoryPathForPartition(rawURL string, parent listingPartition) (string, bool) {
	candidate := parseListingPartition(rawURL)
	if parent.station == "" ||
		candidate.station != parent.station ||
		candidate.category == "" ||
		candidate.category == parent.category {
		return "", false
	}
	return absoluteListingURL(rawURL), true
}

// fetchStationCategoryPaths discovers the cuisine groups exposed for a
// station listing that is still capped after station partitioning.
func fetchStationCategoryPaths(cappedPath string) ([]string, error) {
	targetURL := absoluteListingURL(cappedPath)
	parent := parseListingPartition(targetURL)
	if parent.station == "" || parent.category == "" {
		return nil, fmt.Errorf("cannot cuisine-partition non-station listing %s", targetURL)
	}

	var paths []string
	var pathsMu sync.Mutex
	c := newCollectionCollector(3 * time.Second)
	retrier := newRequestRetrier(defaultCollectionRetryPolicy, nil)
	retrier.Attach(c)

	// Restrict discovery to the cuisine list for the currently selected
	// category. Using every link on the page also captures sibling cuisine
	// groups from unrelated navigation and cannot split a capped group such as
	// washoku into its leaf cuisines.
	c.OnHTML("#js-leftnavi-genre-balloon .list-balloon__list a[href]", func(element *colly.HTMLElement) {
		candidateURL := element.Request.AbsoluteURL(element.Attr("href"))
		categoryPath, ok := stationCategoryPathForPartition(candidateURL, parent)
		if !ok {
			return
		}
		pathsMu.Lock()
		paths = append(paths, categoryPath)
		pathsMu.Unlock()
	})

	if err := visitWithRetries(c, targetURL, retrier); err != nil {
		return nil, fmt.Errorf("discover station categories for %s: %w", targetURL, err)
	}
	paths = dedupeStrings(paths)
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no cuisine paths found for capped station listing %s", targetURL)
	}
	fmt.Printf("Found %d cuisine paths for capped station listing: %s\n", len(paths), targetURL)
	return paths, nil
}

// fetchStationPaths discovers the station filters Tabelog exposes for a
// capped sub-area × cuisine listing. A station path is one geographic level
// finer than a sub-area and retains the selected cuisine.
func fetchStationPaths(cappedPath string) ([]string, error) {
	targetURL := absoluteListingURL(cappedPath)
	parent := parseListingPartition(targetURL)
	if parent.subArea == "" || parent.category == "" {
		return nil, fmt.Errorf("cannot station-partition non sub-area cuisine listing %s", targetURL)
	}

	var paths []string
	var pathsMu sync.Mutex
	c := newCollectionCollector(3 * time.Second)
	retrier := newRequestRetrier(defaultCollectionRetryPolicy, nil)
	retrier.Attach(c)

	c.OnHTML("a[href]", func(element *colly.HTMLElement) {
		candidateURL := element.Request.AbsoluteURL(element.Attr("href"))
		stationPath, ok := stationPathForPartition(candidateURL, parent)
		if !ok {
			return
		}
		pathsMu.Lock()
		paths = append(paths, stationPath)
		pathsMu.Unlock()
	})

	if err := visitWithRetries(c, targetURL, retrier); err != nil {
		return nil, fmt.Errorf("discover station paths for %s: %w", targetURL, err)
	}
	paths = dedupeStrings(paths)
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no station paths found for capped listing %s", targetURL)
	}
	fmt.Printf("Found %d station paths for capped listing: %s\n", len(paths), targetURL)
	return paths, nil
}

// stationFallbackPathsForCaps adds a third partition level for any sub-area ×
// cuisine fallback that still reaches Tabelog's page limit.
func stationFallbackPathsForCaps(outcomes []crawlOutcome) ([]string, error) {
	var cappedTargets []string
	for _, outcome := range outcomes {
		if outcome.hitPageCap {
			cappedTargets = append(cappedTargets, outcome.target)
		}
	}
	cappedTargets = dedupeStrings(cappedTargets)
	if len(cappedTargets) == 0 {
		return nil, nil
	}

	results := make([][]string, len(cappedTargets))
	errs := make([]error, len(cappedTargets))
	var waitGroup sync.WaitGroup
	semaphore := make(chan struct{}, discoverWorkers)
	for index, target := range cappedTargets {
		waitGroup.Add(1)
		semaphore <- struct{}{}
		go func(resultIndex int, cappedTarget string) {
			defer waitGroup.Done()
			defer func() { <-semaphore }()
			results[resultIndex], errs[resultIndex] = fetchStationPaths(cappedTarget)
		}(index, target)
	}
	waitGroup.Wait()

	var paths []string
	var discoveryErrs []error
	for index := range results {
		paths = append(paths, results[index]...)
		if errs[index] != nil {
			discoveryErrs = append(discoveryErrs, errs[index])
		}
	}
	return dedupeStrings(paths), errors.Join(discoveryErrs...)
}

func outcomeErrors(outcomes []crawlOutcome) error {
	var errs []error
	for _, outcome := range outcomes {
		if outcome.collectionErr != nil {
			errs = append(errs, outcome.collectionErr)
		}
	}
	return errors.Join(errs...)
}

// FetchRstUrls collects all restaurant URLs for a prefecture into filename.
// Existing URLs are retained. Transient requests are retried with exponential
// backoff, exhausted listing pages are journaled for the next run, and capped
// listings are split into sub-area × cuisine fallback paths, then station ×
// cuisine paths when a fallback is also capped.
func FetchRstUrls(prefecture string, filename string) error {
	fmt.Println("Fetching areas for prefecture:", prefecture)
	areas, err := fetchAreaPaths(prefecture)
	if err != nil {
		return err
	}
	if len(areas) == 0 {
		return fmt.Errorf("no areas found for prefecture %q — check the prefecture slug", prefecture)
	}
	fmt.Printf("Found %d areas\n", len(areas))

	discoveries, err := discoverAreaPartitions(areas)
	if err != nil {
		return fmt.Errorf("partition discovery incomplete: %w", err)
	}

	var initialPaths []string
	for _, discovery := range discoveries {
		initialPaths = append(initialPaths, discovery.subAreas...)
		initialPaths = append(initialPaths, discovery.categoryPaths...)
	}
	initialPaths = dedupeStrings(initialPaths)
	fmt.Printf("Initial crawl paths: %d\n", len(initialPaths))

	collected, err := ensureURLFile(filename)
	if err != nil {
		return fmt.Errorf("load URL file %s: %w", filename, err)
	}
	fmt.Printf("Loaded %d existing URLs from %s\n", len(collected), filename)

	failedFilename := failedPathsFilename(filename)
	failedStore, err := openFailedPathStore(failedFilename)
	if err != nil {
		return err
	}
	resumePaths := failedStore.Snapshot()
	var collectedMu sync.Mutex
	var resumeOutcomes []crawlOutcome
	if len(resumePaths) > 0 {
		fmt.Printf("Resuming %d failed listing pages from %s\n", len(resumePaths), failedFilename)
		resumeOutcomes = crawlBatch(resumePaths, filename, collected, &collectedMu, failedStore)
		// A resumed page continues pagination from the exact failure point. The
		// earlier pages in that partition were already written in the prior run,
		// so avoid starting the same partition from page 1 concurrently.
		initialPaths = pathsWithoutResumedPartitions(initialPaths, resumePaths)
	}

	initialOutcomes := append(
		resumeOutcomes,
		crawlBatch(initialPaths, filename, collected, &collectedMu, failedStore)...,
	)

	fallbackPaths := fallbackPathsForCaps(initialOutcomes, discoveries)
	var fallbackOutcomes []crawlOutcome
	if len(fallbackPaths) > 0 {
		fmt.Printf(
			"Detected capped listings; crawling %d sub-area × cuisine fallback paths\n",
			len(fallbackPaths),
		)
		fallbackOutcomes = crawlBatch(fallbackPaths, filename, collected, &collectedMu, failedStore)
	}

	stationFallbackPaths, err := stationFallbackPathsForCaps(fallbackOutcomes)
	if err != nil {
		return fmt.Errorf("station partition discovery incomplete: %w", err)
	}
	var stationFallbackOutcomes []crawlOutcome
	if len(stationFallbackPaths) > 0 {
		fmt.Printf(
			"Fallback listings still capped; crawling %d station × cuisine paths\n",
			len(stationFallbackPaths),
		)
		stationFallbackOutcomes = crawlBatch(
			stationFallbackPaths,
			filename,
			collected,
			&collectedMu,
			failedStore,
		)
	}

	if err := failedStore.Compact(); err != nil {
		return fmt.Errorf("compact failed-path journal: %w", err)
	}

	collectedMu.Lock()
	total := len(collected)
	collectedMu.Unlock()

	unresolved := failedStore.Snapshot()
	if len(unresolved) > 0 {
		fmt.Printf(
			"Incomplete. Collected %d unique restaurant URLs; %d failed listing pages saved to %s\n",
			total,
			len(unresolved),
			failedFilename,
		)
		return errors.Join(
			outcomeErrors(initialOutcomes),
			outcomeErrors(fallbackOutcomes),
			outcomeErrors(stationFallbackOutcomes),
			fmt.Errorf("%d listing pages remain unresolved", len(unresolved)),
		)
	}
	if err := errors.Join(
		outcomeErrors(initialOutcomes),
		outcomeErrors(fallbackOutcomes),
		outcomeErrors(stationFallbackOutcomes),
	); err != nil {
		return err
	}

	var cappedStationFallbacks int
	for _, outcome := range stationFallbackOutcomes {
		if outcome.hitPageCap {
			cappedStationFallbacks++
			fmt.Printf(
				"Coverage warning: station × cuisine listing reached page %d: %s\n",
				listingPageCap,
				outcome.target,
			)
		}
	}
	if cappedStationFallbacks > 0 {
		fmt.Printf(
			"Coverage warning: %d station × cuisine listings also reached page %d\n",
			cappedStationFallbacks,
			listingPageCap,
		)
	}
	fmt.Printf("Done. Collected %d unique restaurant URLs.\n", total)
	return nil
}

// FetchRstUrlsFromCSV reads listing page URLs from srcFile, crawls each one,
// and appends discovered restaurant URLs into destFile.
func FetchRstUrlsFromCSV(srcFile string, destFile string) {
	collected, err := ensureURLFile(destFile)
	if err != nil {
		fmt.Printf("Collection failed: %v\n", err)
		return
	}
	fmt.Printf("Loaded %d existing URLs from %s\n", len(collected), destFile)

	source, err := os.Open(srcFile)
	if err != nil {
		fmt.Printf("Collection failed: %v\n", err)
		return
	}
	records, readErr := csv.NewReader(source).ReadAll()
	closeErr := source.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		fmt.Printf("Collection failed: %v\n", err)
		return
	}

	var listingURLs []string
	for _, row := range records {
		if len(row) == 0 {
			continue
		}
		listingURL := strings.TrimSpace(row[0])
		if listingURL != "" && listingURL != "url" && listingURL != "urls" {
			listingURLs = append(listingURLs, listingURL)
		}
	}

	failedFilename := failedPathsFilename(destFile)
	failedStore, err := openFailedPathStore(failedFilename)
	if err != nil {
		fmt.Printf("Collection failed: %v\n", err)
		return
	}
	listingURLs = append(failedStore.Snapshot(), listingURLs...)

	var collectedMu sync.Mutex
	outcomes := crawlBatch(listingURLs, destFile, collected, &collectedMu, failedStore)
	if err := failedStore.Compact(); err != nil {
		fmt.Printf("Collection failed: %v\n", err)
		return
	}

	unresolved := failedStore.Snapshot()
	if len(unresolved) > 0 || outcomeErrors(outcomes) != nil {
		fmt.Printf(
			"Collection incomplete: %d failed listing pages saved to %s\n",
			len(unresolved),
			failedFilename,
		)
		return
	}
	fmt.Printf("Done. Total unique restaurant URLs in %s: %d\n", destFile, len(collected))
}
