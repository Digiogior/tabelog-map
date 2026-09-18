// pillow-ja walks Japanese list pages to harvest each restaurant's PR title
// (pillow word) and backfills restaurants.pillow_ja.
//
// It groups restaurant URLs by sub-area (e.g. "aichi/A2301/A230105"), then
// for each sub-area walks https://tabelog.com/<sub-area>/rstLst/ with
// pagination. The worklist is naturally idempotent: only sub-areas containing
// rows where pillow_ja IS NULL are processed.
package main

import (
	"database/sql"
	"flag"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tabelog-map/internal/scraper"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultWorkers = 3
	maxRetries     = 3
	backoffDelay   = 45 * time.Second
)

func main() {
	dsnFlag := flag.String("db", "", "PostgreSQL DSN (default: DATABASE_URL env, then localhost)")
	workers := flag.Int("workers", defaultWorkers, "Number of concurrent listing walkers")
	limit := flag.Int("limit", 0, "Optional max number of sub-areas to process (0 = no limit)")
	flag.Parse()

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

	subAreas, err := loadSubAreas(conn, *limit)
	if err != nil {
		log.Fatal("load sub-areas:", err)
	}
	total := len(subAreas)
	log.Printf("Pending: %d sub-areas, workers: %d", total, *workers)
	if total == 0 {
		return
	}

	jobChan := make(chan string, *workers*2)

	var (
		doneAreas    atomic.Int64
		updatedRows  atomic.Int64
		failedAreas  atomic.Int64
		backoffUntil atomic.Int64
		wg           sync.WaitGroup
	)

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for subArea := range jobChan {
				if t := backoffUntil.Load(); t > 0 {
					if wait := time.Until(time.Unix(0, t)); wait > 0 {
						log.Printf("[worker %d] backing off %v", id, wait.Round(time.Second))
						time.Sleep(wait)
					}
				}

				listingURL := "https://tabelog.com/" + subArea + "/"
				var areaUpdated int64

				var fetchErr error
				for attempt := 0; attempt < maxRetries; attempt++ {
					areaUpdated = 0
					fetchErr = scraper.FetchPillowsFromListing(listingURL, func(p scraper.PillowEntry) {
						dbURL := zhURL(p.URL)
						n, err := updatePillow(conn, dbURL, p.Pillow)
						if err != nil {
							log.Printf("[worker %d] update %s: %v", id, dbURL, err)
							return
						}
						areaUpdated += n
					})
					if fetchErr == scraper.ErrRateLimited {
						until := time.Now().Add(backoffDelay).UnixNano()
						backoffUntil.Store(until)
						log.Printf("[worker %d] 429 on %s (attempt %d), backing off %v", id, subArea, attempt+1, backoffDelay)
						time.Sleep(backoffDelay)
						continue
					}
					break
				}

				if fetchErr != nil {
					log.Printf("[worker %d] FAILED %s: %v", id, subArea, fetchErr)
					failedAreas.Add(1)
					continue
				}

				updatedRows.Add(areaUpdated)
				n := doneAreas.Add(1)
				log.Printf("[%d/%d] %s — pillow rows updated: %d", n, total, subArea, areaUpdated)
			}
		}(i)
	}

	for _, s := range subAreas {
		jobChan <- s
	}
	close(jobChan)
	wg.Wait()

	log.Printf("Done. Sub-areas processed: %d, failed: %d, rows updated: %d",
		doneAreas.Load(), failedAreas.Load(), updatedRows.Load())
}

// loadSubAreas returns unique sub-area paths (e.g. "aichi/A2301/A230105")
// for restaurants whose pillow_ja is still NULL.
func loadSubAreas(db *sql.DB, limit int) ([]string, error) {
	q := `
		SELECT DISTINCT regexp_replace(
			url,
			'^https?://tabelog\.com/tw/([^/]+/[^/]+/[^/]+)/.*$',
			'\1'
		) AS sub_area
		FROM restaurants
		WHERE pillow_ja IS NULL
		  AND url ~ '^https?://tabelog\.com/tw/[^/]+/[^/]+/[^/]+/'
	`
	if limit > 0 {
		q += " LIMIT $1"
	}

	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = db.Query(q, limit)
	} else {
		rows, err = db.Query(q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subAreas []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		subAreas = append(subAreas, s)
	}
	return subAreas, rows.Err()
}

// zhURL converts a canonical JP URL back to the /tw/ form stored in the DB.
func zhURL(jpURL string) string {
	return strings.Replace(jpURL, "tabelog.com/", "tabelog.com/tw/", 1)
}

// updatePillow sets pillow_ja for a restaurant if it is currently NULL.
// Returns the number of rows affected.
func updatePillow(db *sql.DB, dbURL, pillow string) (int64, error) {
	res, err := db.Exec(`
		UPDATE restaurants
		SET pillow_ja = $1
		WHERE url = $2 AND pillow_ja IS NULL
	`, pillow, dbURL)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
