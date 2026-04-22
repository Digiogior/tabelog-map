package scraper

import (
	"encoding/csv"
	"fmt"
	"os"
)

const (
	StatusDone  = "done"
	StatusError = "error"
)

// LoadProgress reads crawl_progress.csv and returns a map of url -> status.
// Returns an empty map if the file does not exist yet.
func LoadProgress(progressFile string) map[string]string {
	progress := make(map[string]string)

	f, err := os.Open(progressFile)
	if err != nil {
		return progress
	}
	defer f.Close()

	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return progress
	}

	for _, row := range records {
		if len(row) < 2 || row[0] == "url" {
			continue
		}
		progress[row[0]] = row[1]
	}

	fmt.Printf("Loaded progress: %d done, %d error\n",
		countStatus(progress, StatusDone),
		countStatus(progress, StatusError),
	)

	return progress
}

// RecordProgress appends a url,status row to progressFile.
func RecordProgress(progressFile string, url string, status string) {
	f, err := os.OpenFile(progressFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("warning: could not write progress for %s: %v\n", url, err)
		return
	}
	defer f.Close()

	// Write header if file is new (size == 0)
	info, _ := f.Stat()
	w := csv.NewWriter(f)
	if info.Size() == 0 {
		w.Write([]string{"url", "status"})
	}
	w.Write([]string{url, status})
	w.Flush()
}

func countStatus(progress map[string]string, status string) int {
	n := 0
	for _, s := range progress {
		if s == status {
			n++
		}
	}
	return n
}
