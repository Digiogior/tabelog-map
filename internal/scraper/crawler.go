package scraper

import (
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"tabelog-map/models"
	"time"

	"github.com/gocolly/colly"
)

// ErrRateLimited is returned by FetchRstInfo when the server responds with HTTP 429.
var ErrRateLimited = errors.New("rate limited (HTTP 429)")

const (
	discoverWorkers = 3 // concurrent area path discovery
	crawlWorkers    = 3 // concurrent restaurant-listing crawlers
)

func newCollector(delay time.Duration) *colly.Collector {
	c := colly.NewCollector(colly.AllowedDomains("tabelog.com"))
	c.Limit(&colly.LimitRule{DomainGlob: "*", Delay: delay, Parallelism: 1})
	c.OnRequest(func(r *colly.Request) { r.Headers.Set("User-Agent", "MyResearchBot/1.0") })
	return c
}

func segCount(path string) int {
	return len(strings.FieldsFunc(strings.TrimSuffix(path, "/"), func(r rune) bool { return r == '/' }))
}

// fetchSubAreaPaths visits the area listing page and returns only valid sub-area paths
// (exactly one segment deeper than city, starting with "A", e.g. "tokyo/A1301/A130101/").
// Restaurant pages and pagination links are filtered out.
func fetchSubAreaPaths(city string) []string {
	targetUrl := "https://tabelog.com/tw/" + city
	subAreas := []string{city}
	seen := map[string]bool{city: true}
	citySegs := segCount(city)

	c := newCollector(3 * time.Second)

	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		href := e.Attr("href")
		href = strings.TrimPrefix(href, "https://tabelog.com")
		href = strings.TrimPrefix(href, "/tw/")
		if !strings.HasPrefix(href, city) || href == city {
			return
		}
		path := strings.TrimSuffix(href, "/") + "/"
		segs := strings.FieldsFunc(strings.TrimSuffix(path, "/"), func(r rune) bool { return r == '/' })
		// Accept only paths with exactly one more segment than city,
		// where the new segment is a sub-area code (starts with "A").
		if len(segs) == citySegs+1 && strings.HasPrefix(segs[len(segs)-1], "A") && !seen[path] {
			seen[path] = true
			subAreas = append(subAreas, path)
			fmt.Println("Found sub-area:", path)
		}
	})

	c.Visit(targetUrl)
	return subAreas
}

// fetchAreaPaths visits the prefecture top-level page and returns all area paths
// (e.g. ["tokyo/A1301/", "tokyo/A1302/", ...]).
func fetchAreaPaths(prefecture string) []string {
	targetUrl := "https://tabelog.com/tw/" + prefecture + "/"
	var areas []string
	seen := map[string]bool{}

	c := newCollector(3 * time.Second)

	c.OnHTML("#tabs-panel-balloon-pref-area a.c-link-arrow", func(e *colly.HTMLElement) {
		href := e.Attr("href")
		href = strings.TrimPrefix(href, "/tw/")
		// href looks like "tokyo/A1301/rstLst/?SrtT=rt" — strip from /rstLst onward
		if idx := strings.Index(href, "/rstLst"); idx != -1 {
			href = href[:idx+1] // "tokyo/A1301/"
		}
		if href != "" && !seen[href] {
			seen[href] = true
			areas = append(areas, href)
			fmt.Println("Found area:", href)
		}
	})

	c.Visit(targetUrl)
	return areas
}

// fetchCategoryPaths visits an area listing page and returns per-category listing paths
// (e.g. "tokyo/A1303/rstLst/washoku/?SrtT=rt"). These bypass Tabelog's 60-page-per-listing
// cap by splitting restaurants by food type.
func fetchCategoryPaths(areaPath string) []string {
	targetUrl := "https://tabelog.com/tw/" + areaPath + "rstLst/?SrtT=rt"
	var paths []string
	seen := map[string]bool{}

	c := newCollector(3 * time.Second)

	c.OnHTML("#js-leftnavi-genre-scroll a.list-balloon__btn", func(e *colly.HTMLElement) {
		href := e.Attr("href")
		href = strings.TrimPrefix(href, "/tw/")
		if strings.Contains(href, "/rstLst/") && !seen[href] {
			seen[href] = true
			paths = append(paths, href)
		}
	})

	c.Visit(targetUrl)
	return paths
}

// fetchUrlsFromArea crawls a listing page (with pagination) and appends discovered
// restaurant URLs to filename. collected and mu guard against duplicates across
// concurrent workers.
func fetchUrlsFromArea(city string, filename string, collected map[string]bool, mu *sync.Mutex) {
	visitedPages := make(map[string]bool)
	targetUrl := "https://tabelog.com/tw/" + city

	c := newCollector(5 * time.Second)

	c.OnHTML("a.list-rst__rst-name-target.cpy-rst-name", func(e *colly.HTMLElement) {
		rstUrl := e.Attr("href")

		mu.Lock()
		if collected[rstUrl] {
			mu.Unlock()
			return
		}
		collected[rstUrl] = true

		file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			mu.Unlock()
			log.Fatal(err)
		}
		writer := csv.NewWriter(file)
		writer.Write([]string{rstUrl})
		writer.Flush()
		file.Close()
		mu.Unlock()
	})

	c.OnHTML("a.c-pagination__arrow--next", func(e *colly.HTMLElement) {
		nextPage := e.Attr("href")
		if !visitedPages[nextPage] {
			fmt.Println("scraping:", nextPage)
			visitedPages[nextPage] = true
			e.Request.Visit(nextPage)
		}
	})

	c.OnError(func(r *colly.Response, err error) {
		fmt.Println("Error on:", r.Request.URL)
		fmt.Println("Status Code:", r.StatusCode)
		if r.StatusCode == 429 {
			fmt.Println("Retry-After:", r.Headers.Get("Retry-After"))
		}
		fmt.Println("Error:", err)
	})

	c.Visit(targetUrl)
}

// FetchRstUrls collects all restaurant URLs for a prefecture into filename.
// Discovery (prefecture → areas → sub-areas + categories) runs with discoverWorkers
// parallel goroutines; URL crawling runs with crawlWorkers parallel goroutines.
func FetchRstUrls(prefecture string, filename string) {
	fmt.Println("Fetching areas for prefecture:", prefecture)
	areas := fetchAreaPaths(prefecture)
	if len(areas) == 0 {
		log.Fatalf("no areas found for prefecture %q — check the prefecture slug", prefecture)
	}
	fmt.Printf("Found %d areas\n", len(areas))

	// Parallel discovery: sub-areas + categories per area
	type areaResult struct {
		subAreas []string
		catPaths []string
	}
	results := make([]areaResult, len(areas))

	var discoverWg sync.WaitGroup
	discoverSem := make(chan struct{}, discoverWorkers)

	for i, area := range areas {
		discoverWg.Add(1)
		discoverSem <- struct{}{}
		go func(idx int, a string) {
			defer discoverWg.Done()
			defer func() { <-discoverSem }()
			fmt.Printf("[%d/%d] Discovering paths for area: %s\n", idx+1, len(areas), a)
			results[idx] = areaResult{
				subAreas: fetchSubAreaPaths(a),
				catPaths: fetchCategoryPaths(a),
			}
		}(i, area)
	}
	discoverWg.Wait()

	var crawlPaths []string
	for _, r := range results {
		crawlPaths = append(crawlPaths, r.subAreas...)
		crawlPaths = append(crawlPaths, r.catPaths...)
	}
	fmt.Printf("Total crawl paths: %d\n", len(crawlPaths))

	// Load existing URLs for deduplication
	collected := make(map[string]bool)
	if existing, err := os.Open(filename); err == nil {
		reader := csv.NewReader(existing)
		records, _ := reader.ReadAll()
		existing.Close()
		for _, row := range records {
			if len(row) > 0 {
				collected[row[0]] = true
			}
		}
		fmt.Printf("Loaded %d existing URLs from %s\n", len(collected), filename)
	} else {
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		writer := csv.NewWriter(file)
		writer.Write([]string{"url"})
		writer.Flush()
		file.Close()
	}

	// Parallel crawl
	var mu sync.Mutex
	var crawlWg sync.WaitGroup
	crawlSem := make(chan struct{}, crawlWorkers)

	for i, path := range crawlPaths {
		crawlWg.Add(1)
		crawlSem <- struct{}{}
		go func(idx int, p string) {
			defer crawlWg.Done()
			defer func() { <-crawlSem }()
			fmt.Printf("[%d/%d] Crawling: %s\n", idx+1, len(crawlPaths), p)
			fetchUrlsFromArea(p, filename, collected, &mu)
		}(i, path)
	}
	crawlWg.Wait()

	mu.Lock()
	total := len(collected)
	mu.Unlock()
	fmt.Printf("Done. Collected %d unique restaurant URLs.\n", total)
}

func fetchUrlsFromFullURL(targetUrl string, destFile string, collected map[string]bool, mu *sync.Mutex) {
	visitedPages := make(map[string]bool)

	c := newCollector(5 * time.Second)

	c.OnHTML("a.list-rst__rst-name-target.cpy-rst-name", func(e *colly.HTMLElement) {
		rstUrl := e.Attr("href")

		mu.Lock()
		if collected[rstUrl] {
			mu.Unlock()
			return
		}
		collected[rstUrl] = true

		file, err := os.OpenFile(destFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			mu.Unlock()
			log.Fatal(err)
		}
		writer := csv.NewWriter(file)
		writer.Write([]string{rstUrl})
		writer.Flush()
		file.Close()
		mu.Unlock()
	})

	c.OnHTML("a.c-pagination__arrow--next", func(e *colly.HTMLElement) {
		nextPage := e.Attr("href")
		if !visitedPages[nextPage] {
			fmt.Println("scraping:", nextPage)
			visitedPages[nextPage] = true
			e.Request.Visit(nextPage)
		}
	})

	c.OnError(func(r *colly.Response, err error) {
		fmt.Println("Error on:", r.Request.URL)
		fmt.Println("Status Code:", r.StatusCode)
		if r.StatusCode == 429 {
			fmt.Println("Retry-After:", r.Headers.Get("Retry-After"))
		}
		fmt.Println("Error:", err)
	})

	c.Visit(targetUrl)
}

// FetchRstUrlsFromCSV reads listing page URLs from srcFile, crawls each one
// (with pagination), and appends discovered restaurant URLs into destFile.
func FetchRstUrlsFromCSV(srcFile string, destFile string) {
	collected := make(map[string]bool)
	var mu sync.Mutex

	if f, err := os.Open(destFile); err == nil {
		reader := csv.NewReader(f)
		records, _ := reader.ReadAll()
		f.Close()
		for _, row := range records {
			if len(row) > 0 {
				collected[row[0]] = true
			}
		}
		fmt.Printf("Loaded %d existing URLs from %s\n", len(collected), destFile)
	} else {
		f, err := os.OpenFile(destFile, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		w := csv.NewWriter(f)
		w.Write([]string{"url"})
		w.Flush()
		f.Close()
	}

	src, err := os.Open(srcFile)
	if err != nil {
		log.Fatalf("failed to open %s: %v", srcFile, err)
	}
	defer src.Close()

	records, err := csv.NewReader(src).ReadAll()
	if err != nil {
		log.Fatalf("failed to read %s: %v", srcFile, err)
	}

	for i, row := range records {
		if len(row) == 0 {
			continue
		}
		listingUrl := strings.TrimSpace(row[0])
		if listingUrl == "" || listingUrl == "urls" {
			continue
		}
		fmt.Printf("[%d/%d] Crawling: %s\n", i, len(records)-1, listingUrl)
		fetchUrlsFromFullURL(listingUrl, destFile, collected, &mu)
	}

	fmt.Printf("Done. Total unique restaurant URLs in %s: %d\n", destFile, len(collected))
}

func FetchRstInfo(url string, city string) (models.Restaurant, error) {
	var rst models.Restaurant
	rst.City = city
	rst.URL = url
	var rateLimited bool

	c1 := newCollector(5 * time.Second)
	c1.OnError(func(r *colly.Response, err error) {
		if r.StatusCode == 429 {
			rateLimited = true
		}
	})

	c1.OnHTML(".rdheader-rstname h2.display-name", func(e *colly.HTMLElement) {
		rst.Name = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rstname .alias", func(e *colly.HTMLElement) {
		rst.Alias = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rstname .pillow-word", func(e *colly.HTMLElement) {
		rst.PillowWord = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rstinfo-table__address", func(e *colly.HTMLElement) {
		rst.Address = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rating__score-val-dtl", func(e *colly.HTMLElement) {
		text := strings.TrimSpace(e.Text)
		if text == "-" || text == "" {
			return
		}
		var err error
		rst.Rating, err = strconv.ParseFloat(text, 64)
		if err != nil {
			fmt.Println("Error converting rating to float64:", err)
		}
	})
	c1.OnHTML(".rdheader-budget", func(e *colly.HTMLElement) {
		e.ForEach(".rdheader-budget__price-target", func(i int, e1 *colly.HTMLElement) {
			separator := "-"

			cleanAndParse := func(s string) int {
				s = strings.TrimPrefix(s, "JPY ")
				s = strings.ReplaceAll(s, ",", "")
				val, _ := strconv.Atoi(s)
				return val
			}

			if i == 0 {
				parts := strings.Split(e1.Text, separator)
				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				rst.DinnerMinPrice = cleanAndParse(parts[0])
				rst.DinnerMaxPrice = cleanAndParse(parts[1])
			} else {
				parts := strings.Split(e1.Text, separator)
				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				rst.LunchMinPrice = cleanAndParse(parts[0])
				rst.LunchMaxPrice = cleanAndParse(parts[1])
			}
		})
	})

	c1.OnHTML("dl.rdheader-subinfo__item", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText("dt.rdheader-subinfo__item-title"))
		if title == "最近的車站：" {
			rst.NearestStation = strings.TrimSpace(e.ChildText(".linktree__parent-target-text"))
		}
	})

	c1.OnHTML("dl.rdheader-subinfo__item", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText("dt.rdheader-subinfo__item-title"))
		if title == "類別：" {
			e.ForEach(".linktree__parent-target-text", func(_ int, el *colly.HTMLElement) {
				rst.Categories = append(rst.Categories, strings.TrimSpace(el.Text))
			})
		}
	})

	c1.OnHTML("table.rstinfo-table__table tr", func(e *colly.HTMLElement) {
		if strings.TrimSpace(e.ChildText("th")) == "關於兒童" {
			rst.Kids = strings.TrimSpace(e.ChildText("td p"))
		}
	})

	c1.OnHTML("p.rstdtl-top-postphoto__photo a.js-imagebox-trigger", func(e *colly.HTMLElement) {
		if len(rst.Photos) < 3 {
			if href := strings.TrimSpace(e.Attr("href")); href != "" {
				rst.Photos = append(rst.Photos, href)
			}
		}
	})

	c1.Visit(url)

	if rateLimited {
		return rst, ErrRateLimited
	}

	c2 := newCollector(5 * time.Second)
	c2.OnError(func(r *colly.Response, err error) {
		if r.StatusCode == 429 {
			rateLimited = true
		}
	})

	c2.OnHTML("#js-basics", func(e *colly.HTMLElement) {
		rst.Latitude, _ = strconv.ParseFloat(e.Attr("data-lat"), 64)
		rst.Longitude, _ = strconv.ParseFloat(e.Attr("data-lng"), 64)
	})

	c2.Visit(url + "/dtlmap/")

	if rateLimited {
		return rst, ErrRateLimited
	}

	return rst, nil
}
