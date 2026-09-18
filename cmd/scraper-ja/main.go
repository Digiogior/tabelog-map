// scraper-ja backfills Japanese-language fields on existing restaurant rows
// by fetching the canonical (non-/tw/) tabelog detail page for each restaurant.
//
// The worklist is naturally idempotent: rows where name_ja IS NULL are picked
// up on every run, so failures simply get retried next time.
package main

import (
	"database/sql"
	"flag"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"tabelog-map/internal/scraper"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultWorkers = 10
	maxRetries     = 3
	backoffDelay   = 45 * time.Second
)

type job struct {
	id  int64
	url string
}

func main() {
	dsnFlag := flag.String("db", "", "PostgreSQL DSN (default: DATABASE_URL env, then localhost)")
	workers := flag.Int("workers", defaultWorkers, "Number of concurrent scrape workers")
	limit := flag.Int("limit", 0, "Optional max number of rows to backfill (0 = no limit)")
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

	jobs, err := loadPending(conn, *limit)
	if err != nil {
		log.Fatal("load pending:", err)
	}
	total := len(jobs)
	log.Printf("Pending: %d rows, workers: %d", total, *workers)
	if total == 0 {
		return
	}

	jobChan := make(chan job, *workers*2)

	var (
		doneCnt      atomic.Int64
		failedCnt    atomic.Int64
		backoffUntil atomic.Int64
		wg           sync.WaitGroup
	)

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range jobChan {
				if t := backoffUntil.Load(); t > 0 {
					if wait := time.Until(time.Unix(0, t)); wait > 0 {
						log.Printf("[worker %d] backing off %v", id, wait.Round(time.Second))
						time.Sleep(wait)
					}
				}

				jaURL := scraper.JaURL(j.url)
				var info scraper.RestaurantJa
				var fetchErr error
				for attempt := 0; attempt < maxRetries; attempt++ {
					info, fetchErr = scraper.FetchRstInfoJa(jaURL)
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
					log.Printf("[worker %d] fetch failed for %s: %v", id, jaURL, fetchErr)
					failedCnt.Add(1)
					continue
				}

				if info.Name == "" {
					log.Printf("[worker %d] empty JP name for %s — page may not exist", id, jaURL)
					failedCnt.Add(1)
					continue
				}

				if err := updateRow(conn, j.id, info); err != nil {
					log.Printf("[worker %d] update failed for id=%d: %v", id, j.id, err)
					failedCnt.Add(1)
					continue
				}

				n := doneCnt.Add(1)
				log.Printf("[%d/%d] %s", n, total, info.Name)
			}
		}(i)
	}

	for _, j := range jobs {
		jobChan <- j
	}
	close(jobChan)
	wg.Wait()

	log.Printf("Done. Backfilled: %d, Failed: %d", doneCnt.Load(), failedCnt.Load())
}

func loadPending(db *sql.DB, limit int) ([]job, error) {
	q := `SELECT id, url FROM restaurants WHERE name_ja IS NULL AND url IS NOT NULL`
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

	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.url); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func updateRow(db *sql.DB, id int64, info scraper.RestaurantJa) error {
	_, err := db.Exec(`
		UPDATE restaurants SET
			name_ja             = NULLIF($1, ''),
			alias_ja            = NULLIF($2, ''),
			address_ja          = NULLIF($3, ''),
			nearest_station_ja  = NULLIF($4, ''),
			kids_ja             = NULLIF($5, '')
		WHERE id = $6
	`, info.Name, info.Alias, info.Address, info.NearestStation, info.Kids, id)
	return err
}
