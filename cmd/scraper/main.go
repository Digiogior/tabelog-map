package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tabelog-map/csvutils"
	"tabelog-map/db"
	"tabelog-map/internal/scraper"
	"tabelog-map/models"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultWorkers = 10
	maxRetries     = 3
	backoffDelay   = 45 * time.Second
)

func main() {
	prefecture   := flag.String("prefecture", "", "Tabelog prefecture slug, e.g. tokyo or aichi (required)")
	urlsFileFlag := flag.String("urls-file", "", "Input CSV of restaurant URLs (default: {Prefecture}RstUrls.csv)")
	dsnFlag      := flag.String("db", "", "PostgreSQL DSN (default: DATABASE_URL env, then localhost)")
	retryErrors  := flag.Bool("retry-errors", false, "Retry URLs that previously failed")
	workers      := flag.Int("workers", defaultWorkers, "Number of concurrent scrape workers")
	collectURLs  := flag.Bool("collect-urls", false, "Run URL collection only, then exit")
	flag.Parse()

	if *prefecture == "" {
		fmt.Fprintln(os.Stderr, "usage: scraper --prefecture <slug> [--collect-urls] [--retry-errors] [--workers N]")
		os.Exit(1)
	}

	prefTitle := strings.ToUpper((*prefecture)[:1]) + (*prefecture)[1:]
	urlsFile := *urlsFileFlag
	if urlsFile == "" {
		urlsFile = prefTitle + "RstUrls.csv"
	}

	if *collectURLs {
		scraper.FetchRstUrls(*prefecture, urlsFile)
		return
	}

	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		dsn = "postgres://postgres:password@localhost:5432/tabelog"
	}

	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(*workers + 5)
	conn.SetMaxIdleConns(*workers)

	if err := conn.Ping(); err != nil {
		log.Fatal("failed to connect to postgres:", err)
	}
	if err := db.CreateTables(conn); err != nil {
		log.Fatal("failed to create tables:", err)
	}

	// Seed scrape_progress from the URL CSV (ON CONFLICT DO NOTHING preserves existing rows)
	rows, err := csvutils.ReadCSVAll(urlsFile)
	if err != nil {
		log.Fatal(err)
	}
	var urls []string
	for i, row := range rows {
		if i == 0 || len(row) == 0 {
			continue
		}
		if u := strings.TrimSpace(row[0]); u != "" {
			urls = append(urls, u)
		}
	}
	log.Printf("Seeding %d URLs into scrape_progress...", len(urls))
	if err := db.SeedProgress(conn, urls); err != nil {
		log.Fatal("seed progress:", err)
	}

	if *retryErrors {
		if err := db.ResetErrors(conn); err != nil {
			log.Fatal("reset errors:", err)
		}
	}

	pending, err := db.LoadPendingURLs(conn)
	if err != nil {
		log.Fatal(err)
	}
	total := len(pending)
	log.Printf("Pending: %d URLs, workers: %d", total, *workers)

	urlChan := make(chan string, *workers*2)

	var (
		doneCnt      atomic.Int64
		failedCnt    atomic.Int64
		backoffUntil atomic.Int64 // unix nanoseconds; all workers pause until this time on 429
		wg           sync.WaitGroup
	)

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for url := range urlChan {
				// Honour global 429 backoff set by any worker
				if t := backoffUntil.Load(); t > 0 {
					if wait := time.Until(time.Unix(0, t)); wait > 0 {
						log.Printf("[worker %d] backing off %v", id, wait.Round(time.Second))
						time.Sleep(wait)
					}
				}

				var rst models.Restaurant
				var fetchErr error
				for attempt := 0; attempt < maxRetries; attempt++ {
					rst, fetchErr = scraper.FetchRstInfo(url, *prefecture)
					if fetchErr == scraper.ErrRateLimited {
						until := time.Now().Add(backoffDelay).UnixNano()
						backoffUntil.Store(until)
						log.Printf("[worker %d] 429 on attempt %d, backing off %v", id, attempt+1, backoffDelay)
						time.Sleep(backoffDelay)
						continue
					}
					break
				}

				if fetchErr != nil {
					db.MarkProgress(conn, url, "error", fetchErr.Error())
					failedCnt.Add(1)
					continue
				}

				if _, err := db.InsertRestaurant(conn, rst); err != nil {
					db.MarkProgress(conn, url, "error", err.Error())
					failedCnt.Add(1)
					continue
				}

				db.MarkProgress(conn, url, "done", "")
				n := doneCnt.Add(1)
				log.Printf("[%d/%d] stored: %s", n, total, rst.Name)
			}
		}(i)
	}

	for _, u := range pending {
		urlChan <- u
	}
	close(urlChan)
	wg.Wait()

	log.Printf("Done. Stored: %d, Failed: %d", doneCnt.Load(), failedCnt.Load())
}
