package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gocolly/colly"
)

func main() {

	visitedUrls := make(map[string]bool)

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

	// Get the restaurant list through JSONLD file
	c.OnHTML("a.list-rst__rst-name-target.cpy-rst-name", func(e *colly.HTMLElement) {
		rstUrls := make([]string, 0, 20)
		rstUrl := e.Attr("href")
		rstUrls = append(rstUrls, rstUrl)
		fmt.Println(len(rstUrls))
		file, err := os.OpenFile("rstUrls.csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
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

	c.Visit("https://tabelog.com/tw/tokyo/")

	// c1 := colly.NewCollector(
	// 	colly.AllowedDomains("tabelog.com"),
	// )
	// // Collect restaurant latitude and longitude
	// c1.OnHTML("#js-basics", func(e *colly.HTMLElement) {
	// 	fmt.Println(e.Attr("data-lat"))
	// 	fmt.Println(e.Attr("data-lng"))
	// })
	// // Get restaurant rating
	// c1.OnHTML(".rdheader-rating__score-val-dtl", func(e *colly.HTMLElement) {
	// 	fmt.Println(e.Text)
	// })
	// // Get restaurant lunch/dinner price range
	// c1.OnHTML(".rdheader-budget", func(e *colly.HTMLElement) {
	// 	e.ForEach(".rdheader-budget__price-target", func(i int, e1 *colly.HTMLElement) {
	// 		fmt.Println(e1.Text)
	// 	})
	// })
	// // Get the nearest station and categories
	// c1.OnHTML(".rdheader-subinfo__item-text", func(e *colly.HTMLElement) {
	// 	e.ForEach(".linktree__parent-target-text", func(i int, e1 *colly.HTMLElement) {
	// 		fmt.Println(e1.Text)
	// 	})
	// })
	// c1.Visit("https://tabelog.com/tw/tokyo/A1301/A130101/13263391/dtlmap/")

}
