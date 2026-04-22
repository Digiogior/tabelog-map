package crawler

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

func FetchRstUrls(city string, filename string) {
	visitedUrls := make(map[string]bool)
	targetUrl := "https://tabelog.com/tw/" + city

	c := colly.NewCollector()

	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       5 * time.Second,
		Parallelism: 1,
	})

	c.OnRequest(func(r *colly.Request) {
		ua := "MyResearchBot/1.0"
		r.Headers.Set("User-Agent", ua)
		fmt.Println("Using User-Agent:", ua)
	})

	c.OnHTML("a.list-rst__rst-name-target.cpy-rst-name", func(e *colly.HTMLElement) {
		rstUrls := make([]string, 0, 20)
		rstUrl := e.Attr("href")
		rstUrls = append(rstUrls, rstUrl)
		file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()

		writer := csv.NewWriter(file)
		defer writer.Flush()

		for _, url := range rstUrls {
			writer.Write([]string{url})
		}
	})
	c.OnHTML("a.c-pagination__arrow--next", func(e *colly.HTMLElement) {
		nextPage := e.Attr("href")

		if !visitedUrls[nextPage] {
			fmt.Println("scraping:", nextPage)
			visitedUrls[nextPage] = true
			e.Request.Visit(nextPage)
		}
	})

	// Optional: log failed requests
	c.OnError(func(r *colly.Response, err error) {
		fmt.Println("Error on:", r.Request.URL)
		fmt.Println("Status Code:", r.StatusCode)
		if r.StatusCode == 429 {
			fmt.Println("Retry-After: ", r.Headers.Get("Retry-After"))
		}
		fmt.Println("Error:", err)
	})

	c.Visit(targetUrl)
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

	// Get restaurant name
	c1.OnHTML(".rdheader-rstname h2.display-name", func(e *colly.HTMLElement) {
		rst.Name = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rstname .alias", func(e *colly.HTMLElement) {
		rst.Alias = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rstname .pillow-word", func(e *colly.HTMLElement) {
		rst.PillowWord = strings.TrimSpace(e.Text)
	})
	// Get restaurant address
	c1.OnHTML(".rstinfo-table__address", func(e *colly.HTMLElement) {
		rst.Address = strings.TrimSpace(e.Text)
	})
	// Get restaurant rating
	c1.OnHTML(".rdheader-rating__score-val-dtl", func(e *colly.HTMLElement) {
		var err error
		rst.Rating, err = strconv.ParseFloat(e.Text, 64)
		if err != nil {
			fmt.Println("Error converting rating to float64:", err)
		}
	})
	// Get restaurant lunch/dinner price range
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
				low := cleanAndParse(parts[0])
				high := cleanAndParse(parts[1])

				rst.DinnerMinPrice = low
				rst.DinnerMaxPrice = high
			} else {
				parts := strings.Split(e1.Text, separator)

				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				low := cleanAndParse(parts[0])
				high := cleanAndParse(parts[1])

				rst.LunchMinPrice = low
				rst.LunchMaxPrice = high
			}
		})
	})
	// Get the nearest station

	c1.OnHTML("dl.rdheader-subinfo__item", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText("dt.rdheader-subinfo__item-title"))
		if title == "最近的車站：" {
			station := strings.TrimSpace(e.ChildText(".linktree__parent-target-text"))
			rst.NearestStation = station
		}
	})

	// Get the categories

	c1.OnHTML("dl.rdheader-subinfo__item", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText("dt.rdheader-subinfo__item-title"))
		if title == "類別：" {
			e.ForEach(".linktree__parent-target-text", func(_ int, el *colly.HTMLElement) {
				category := strings.TrimSpace(el.Text)
				rst.Categories = append(rst.Categories, category)
			})
		}
	})

	c1.OnHTML("table.rstinfo-table__table tr", func(e *colly.HTMLElement) {
		if strings.TrimSpace(e.ChildText("th")) == "關於兒童" {
			kids := strings.TrimSpace(e.ChildText("td p"))
			rst.Kids = kids
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

	// Collect restaurant latitude and longitude
	c2.OnHTML("#js-basics", func(e *colly.HTMLElement) {

		rst.Latitude, _ = strconv.ParseFloat(e.Attr("data-lat"), 64)
		rst.Longitude, _ = strconv.ParseFloat(e.Attr("data-lng"), 64)
	})

	mapUrl := url + "/dtlmap/"

	c2.Visit(mapUrl)

	return rst, nil
}
