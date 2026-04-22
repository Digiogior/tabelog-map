package scraper

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"tabelog-map/models"
	"time"

	"github.com/gocolly/colly"
)

func fetchSubAreaPaths(city string) []string {
	targetUrl := "https://tabelog.com/tw/" + city
	subAreas := []string{city}
	seen := map[string]bool{city: true}

	c := colly.NewCollector(
		colly.AllowedDomains("tabelog.com"),
	)
	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       3 * time.Second,
		Parallelism: 1,
	})
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("User-Agent", "MyResearchBot/1.0")
	})

	// Sub-area links appear as /tw/aichi/A2301/A230101/ style hrefs
	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		href := e.Attr("href")
		// Normalise to absolute URL then strip domain prefix
		href = strings.TrimPrefix(href, "https://tabelog.com")
		href = strings.TrimPrefix(href, "/tw/")
		if strings.HasPrefix(href, city) && href != city {
			path := strings.TrimSuffix(href, "/") + "/"
			if !seen[path] {
				seen[path] = true
				subAreas = append(subAreas, path)
				fmt.Println("Found sub-area:", path)
			}
		}
	})

	c.Visit(targetUrl)
	return subAreas
}

func fetchUrlsFromArea(city string, filename string, collected map[string]bool) {
	visitedPages := make(map[string]bool)
	targetUrl := "https://tabelog.com/tw/" + city

	c := colly.NewCollector(
		colly.AllowedDomains("tabelog.com"),
	)
	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       5 * time.Second,
		Parallelism: 1,
	})
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("User-Agent", "MyResearchBot/1.0")
	})

	c.OnHTML("a.list-rst__rst-name-target.cpy-rst-name", func(e *colly.HTMLElement) {
		rstUrl := e.Attr("href")
		if collected[rstUrl] {
			return
		}
		collected[rstUrl] = true

		file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()

		writer := csv.NewWriter(file)
		defer writer.Flush()
		writer.Write([]string{rstUrl})
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

var sortPaths = []string{
	"rstLst/?SrtT=rt",
	"rstLst/?SrtT=inbound_vacancy_net_yoyaku",
	"rstLst/?SrtT=inbound_access",
}

func FetchRstUrls(city string, filename string) {
	fmt.Println("Fetching sub-areas for:", city)
	subAreas := fetchSubAreaPaths(city)

	// Append sort-order listing URLs to expose restaurants beyond the 60-page cap
	cityBase := strings.TrimSuffix(city, "/")
	for _, sort := range sortPaths {
		subAreas = append(subAreas, cityBase+"/"+sort)
	}

	fmt.Printf("Found %d areas to crawl\n", len(subAreas))

	collected := make(map[string]bool)

	// Load existing URLs from CSV into collected map to avoid duplicates
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
		// File doesn't exist yet — create it with header
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		writer := csv.NewWriter(file)
		writer.Write([]string{"url"})
		writer.Flush()
		file.Close()
	}

	for i, area := range subAreas {
		fmt.Printf("[%d/%d] Crawling area: %s\n", i+1, len(subAreas), area)
		fetchUrlsFromArea(area, filename, collected)
	}

	fmt.Printf("Done. Collected %d unique restaurant URLs.\n", len(collected))
}

func fetchUrlsFromFullURL(targetUrl string, destFile string, collected map[string]bool) {
	visitedPages := make(map[string]bool)

	c := colly.NewCollector(
		colly.AllowedDomains("tabelog.com"),
	)
	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       5 * time.Second,
		Parallelism: 1,
	})
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("User-Agent", "MyResearchBot/1.0")
	})

	c.OnHTML("a.list-rst__rst-name-target.cpy-rst-name", func(e *colly.HTMLElement) {
		rstUrl := e.Attr("href")
		if collected[rstUrl] {
			return
		}
		collected[rstUrl] = true

		file, err := os.OpenFile(destFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()

		writer := csv.NewWriter(file)
		defer writer.Flush()
		writer.Write([]string{rstUrl})
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
	// Load existing restaurant URLs from destination to avoid duplicates
	collected := make(map[string]bool)
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
		// destFile doesn't exist yet — create with header
		f, err := os.OpenFile(destFile, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		w := csv.NewWriter(f)
		w.Write([]string{"url"})
		w.Flush()
		f.Close()
	}

	// Read listing URLs from source CSV
	src, err := os.Open(srcFile)
	if err != nil {
		log.Fatalf("failed to open %s: %v", srcFile, err)
	}
	defer src.Close()

	records, err := csv.NewReader(src).ReadAll()
	if err != nil {
		log.Fatalf("failed to read %s: %v", srcFile, err)
	}

	// Crawl each listing URL
	for i, row := range records {
		if len(row) == 0 {
			continue
		}
		listingUrl := strings.TrimSpace(row[0])
		if listingUrl == "" || listingUrl == "urls" {
			continue
		}
		fmt.Printf("[%d/%d] Crawling: %s\n", i, len(records)-1, listingUrl)
		fetchUrlsFromFullURL(listingUrl, destFile, collected)
	}

	fmt.Printf("Done. Total unique restaurant URLs in %s: %d\n", destFile, len(collected))
}

func FetchRstInfo(url string, city string) (models.Restaurant, error) {
	var rst models.Restaurant
	rst.City = city
	rst.URL = url

	c1 := colly.NewCollector(
		colly.AllowedDomains("tabelog.com"),
	)

	c1.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       5 * time.Second,
		Parallelism: 1,
	})

	c1.OnRequest(func(r *colly.Request) {
		ua := "MyResearchBot/1.0"
		r.Headers.Set("User-Agent", ua)
		fmt.Println("Using User-Agent:", ua)
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

	c1.Visit(url)

	c2 := colly.NewCollector(
		colly.AllowedDomains("tabelog.com"),
	)

	c2.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       5 * time.Second,
		Parallelism: 1,
	})

	c2.OnHTML("#js-basics", func(e *colly.HTMLElement) {
		rst.Latitude, _ = strconv.ParseFloat(e.Attr("data-lat"), 64)
		rst.Longitude, _ = strconv.ParseFloat(e.Attr("data-lng"), 64)
	})

	c2.Visit(url + "/dtlmap/")

	return rst, nil
}
