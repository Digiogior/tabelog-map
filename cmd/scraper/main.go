package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"tabelog-map/csvutils"
	"tabelog-map/db"
	"tabelog-map/internal/scraper"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	progressFile = "crawl_progress.csv"
	urlsFile     = "NagoyaRstUrls.csv"
	city         = "aichi/A2301/"
	dsn          = "postgres://postgres:password@localhost:5432/nagoya"
)

func main() {
	retryErrors := flag.Bool("retry-errors", false, "retry URLs that previously failed with an error")
	flag.Parse()

	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	if err := conn.Ping(); err != nil {
		log.Fatal("failed to connect to postgres:", err)
	}

	if err := db.CreateTables(conn); err != nil {
		log.Fatal("failed to create tables:", err)
	}

	// Load crawl progress
	progress := scraper.LoadProgress(progressFile)

	// Read all restaurant URLs
	rows, err := csvutils.ReadCSVAll(urlsFile)
	if err != nil {
		log.Fatal(err)
	}

	total := len(rows) - 1 // exclude header
	done, skipped, failed := 0, 0, 0

	for i, row := range rows {
		if i == 0 || len(row) == 0 {
			continue
		}
		url := row[0]

		status := progress[url]
		if status == scraper.StatusDone {
			skipped++
			continue
		}
		if status == scraper.StatusError && !*retryErrors {
			skipped++
			continue
		}

		fmt.Printf("[%d/%d] Crawling: %s\n", i, total, url)

		rst, err := scraper.FetchRstInfo(url, city)
		if err != nil {
			log.Printf("fetch error for %s: %v\n", url, err)
			scraper.RecordProgress(progressFile, url, scraper.StatusError)
			failed++
			continue
		}

		_, err = db.InsertRestaurant(conn, rst)
		if err != nil {
			log.Printf("db error for %s: %v\n", url, err)
			scraper.RecordProgress(progressFile, url, scraper.StatusError)
			failed++
			continue
		}

		scraper.RecordProgress(progressFile, url, scraper.StatusDone)
		fmt.Printf("Stored: %s\n", rst.Name)
		done++
	}

	fmt.Printf("\nDone. Processed: %d, Skipped: %d, Failed: %d\n", done, skipped, failed)

	// scraper.FetchRstUrlsFromCSV("tabelogUrls.csv", "NagoyaRstUrls.csv")
}
