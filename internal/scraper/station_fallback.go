package scraper

import (
	"errors"
	"fmt"
	"sync"
)

// FetchStationFallbackURLs performs a targeted recovery for a known capped
// sub-area × cuisine listing. It is useful after an older collection run
// reported a coverage warning but did not yet support station partitioning.
func FetchStationFallbackURLs(cappedPath string, filename string) error {
	stationPaths, err := fetchStationPaths(cappedPath)
	if err != nil {
		return err
	}

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
	if len(resumePaths) > 0 {
		fmt.Printf("Resuming %d failed listing pages from %s\n", len(resumePaths), failedFilename)
		stationPaths = pathsWithoutResumedPartitions(stationPaths, resumePaths)
		stationPaths = append(resumePaths, stationPaths...)
	}

	var collectedMu sync.Mutex
	fmt.Printf("Crawling %d station × cuisine paths\n", len(stationPaths))
	outcomes := crawlBatch(stationPaths, filename, collected, &collectedMu, failedStore)
	if err := failedStore.Compact(); err != nil {
		return fmt.Errorf("compact failed-path journal: %w", err)
	}

	unresolved := failedStore.Snapshot()
	if len(unresolved) > 0 {
		return errors.Join(
			outcomeErrors(outcomes),
			fmt.Errorf("%d listing pages remain unresolved", len(unresolved)),
		)
	}
	if err := outcomeErrors(outcomes); err != nil {
		return err
	}

	var cappedStations int
	for _, outcome := range outcomes {
		if !outcome.hitPageCap {
			continue
		}
		cappedStations++
		fmt.Printf(
			"Coverage warning: station × cuisine listing reached page %d: %s\n",
			listingPageCap,
			outcome.target,
		)
	}
	if cappedStations > 0 {
		fmt.Printf(
			"Coverage warning: %d station × cuisine listings also reached page %d\n",
			cappedStations,
			listingPageCap,
		)
	}

	collectedMu.Lock()
	total := len(collected)
	collectedMu.Unlock()
	fmt.Printf("Done. Collected %d unique restaurant URLs.\n", total)
	return nil
}

// FetchStationCategoryFallbackURLs performs a targeted final recovery for a
// station listing that remains capped after station partitioning.
func FetchStationCategoryFallbackURLs(cappedPath string, filename string) error {
	categoryPaths, err := fetchStationCategoryPaths(cappedPath)
	if err != nil {
		return err
	}

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
	if len(resumePaths) > 0 {
		fmt.Printf("Resuming %d failed listing pages from %s\n", len(resumePaths), failedFilename)
		categoryPaths = pathsWithoutResumedPartitions(categoryPaths, resumePaths)
		categoryPaths = append(resumePaths, categoryPaths...)
	}

	var collectedMu sync.Mutex
	var outcomes []crawlOutcome
	paths := categoryPaths
	const maxCuisineDepth = 4
	for depth := 1; len(paths) > 0; depth++ {
		if depth == 1 {
			fmt.Printf("Crawling %d station × cuisine-group paths\n", len(paths))
		} else {
			fmt.Printf("Crawling %d finer station × cuisine paths at depth %d\n", len(paths), depth)
		}

		levelOutcomes := crawlBatch(paths, filename, collected, &collectedMu, failedStore)
		outcomes = append(outcomes, levelOutcomes...)

		var cappedTargets []string
		for _, outcome := range levelOutcomes {
			if outcome.hitPageCap {
				cappedTargets = append(cappedTargets, outcome.target)
			}
		}
		cappedTargets = dedupeStrings(cappedTargets)
		if len(cappedTargets) == 0 {
			break
		}
		if depth >= maxCuisineDepth {
			for _, target := range cappedTargets {
				fmt.Printf(
					"Coverage warning: station × cuisine-group listing reached page %d: %s\n",
					listingPageCap,
					target,
				)
			}
			return fmt.Errorf(
				"%d station × cuisine-group listings still capped after depth %d",
				len(cappedTargets),
				depth,
			)
		}

		var finerPaths []string
		for _, target := range cappedTargets {
			children, childErr := fetchStationCategoryPaths(target)
			if childErr != nil {
				return fmt.Errorf("cannot further partition capped station cuisine %s: %w", target, childErr)
			}
			finerPaths = append(finerPaths, children...)
		}
		paths = dedupeStrings(finerPaths)
	}
	if err := failedStore.Compact(); err != nil {
		return fmt.Errorf("compact failed-path journal: %w", err)
	}

	unresolved := failedStore.Snapshot()
	if len(unresolved) > 0 {
		return errors.Join(
			outcomeErrors(outcomes),
			fmt.Errorf("%d listing pages remain unresolved", len(unresolved)),
		)
	}
	if err := outcomeErrors(outcomes); err != nil {
		return err
	}

	collectedMu.Lock()
	total := len(collected)
	collectedMu.Unlock()
	fmt.Printf("Done. Collected %d unique restaurant URLs.\n", total)
	return nil
}
