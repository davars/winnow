package worker

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"
)

// WorkItem is a unit of work.
type WorkItem struct {
	Hash   string // content hash (empty for sha256 step itself)
	FileID int64  // files.id
	Path   string // absolute path to a readable file
}

// WorkResult is what a worker produces for one item.
type WorkResult struct {
	Item   WorkItem
	Values map[string]any // column values to write back
	Err    error          // non-nil → log to process_errors, skip this item
}

// WorkSource abstracts DB reads and writes for a specific job type.
type WorkSource interface {
	// FetchBatch returns the next batch of pending items.
	// Quick read, no long-lived transaction. Returns empty slice when done.
	FetchBatch(ctx context.Context, db *sql.DB, limit int) ([]WorkItem, error)

	// WriteBatch writes completed results in a single transaction.
	WriteBatch(ctx context.Context, db *sql.DB, results []WorkResult) error
}

// ProcessFunc processes a batch of items. Called by each worker goroutine.
// Must return exactly one WorkResult per input item.
type ProcessFunc func(ctx context.Context, items []WorkItem) []WorkResult

// Opts configures the worker pool.
type Opts struct {
	Workers      int // concurrent worker goroutines (default: runtime.NumCPU())
	BatchSize    int // items per DB fetch (default: Workers * ProcessBatch)
	ProcessBatch int // items per ProcessFunc call (default: 1)
}

func (o Opts) workers() int {
	if o.Workers > 0 {
		return o.Workers
	}
	return runtime.NumCPU()
}

func (o Opts) processBatch() int {
	if o.ProcessBatch > 0 {
		return o.ProcessBatch
	}
	return 1
}

func (o Opts) batchSize(workers int) int {
	if o.BatchSize > 0 {
		return o.BatchSize
	}
	return workers * o.processBatch()
}

// Stats is returned when the pool completes.
type Stats struct {
	Processed int
	Errors    int
	Duration  time.Duration
}

// chunkResult carries either successful results or a panic from a worker goroutine.
type chunkResult struct {
	results []WorkResult
	panVal  any // non-nil if the worker panicked
}

// Run executes the worker pool to completion or context cancellation.
// Prints progress to stdout if >7s since last update.
// Panics if ProcessFunc returns wrong number of results.
// On context cancellation: finishes in-flight chunk, writes completed results, returns.
func Run(ctx context.Context, db *sql.DB, source WorkSource, process ProcessFunc, opts Opts) (Stats, error) {
	start := time.Now()
	numWorkers := opts.workers()
	procBatch := opts.processBatch()
	batchSize := opts.batchSize(numWorkers)

	var stats Stats
	lastProgress := time.Now()

	for {
		if ctx.Err() != nil {
			stats.Duration = time.Since(start)
			return stats, ctx.Err()
		}

		items, err := source.FetchBatch(ctx, db, batchSize)
		if err != nil {
			stats.Duration = time.Since(start)
			return stats, fmt.Errorf("fetching batch: %w", err)
		}
		if len(items) == 0 {
			break
		}

		numChunks := (len(items) + procBatch - 1) / procBatch

		// Buffer results to numChunks so workers never block on send,
		// preventing deadlock with the coordinator's sends to work.
		work := make(chan []WorkItem, numWorkers)
		results := make(chan chunkResult, numChunks)

		var wg sync.WaitGroup
		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for chunk := range work {
					cr := func() (cr chunkResult) {
						defer func() {
							if r := recover(); r != nil {
								cr = chunkResult{panVal: r}
							}
						}()
						res := process(ctx, chunk)
						if len(res) != len(chunk) {
							panic(fmt.Sprintf("ProcessFunc returned %d results for %d items", len(res), len(chunk)))
						}
						return chunkResult{results: res}
					}()
					results <- cr
				}
			}()
		}

		// Send all chunks from the coordinator. This is safe because
		// workers drain work and never block on results (buffered to numChunks).
		for chunk := range slices.Chunk(items, procBatch) {
			work <- chunk
		}
		close(work)

		// Wait for all workers to finish, then close results.
		// This ensures no goroutine leak even if we re-panic below.
		wg.Wait()
		close(results)

		// Collect results and check for panics.
		allResults := make([]WorkResult, 0, len(items))
		var panVal any
		for cr := range results {
			if cr.panVal != nil {
				panVal = cr.panVal
				continue
			}
			allResults = append(allResults, cr.results...)
		}
		if panVal != nil {
			panic(panVal)
		}

		if err := source.WriteBatch(ctx, db, allResults); err != nil {
			stats.Duration = time.Since(start)
			return stats, fmt.Errorf("writing batch: %w", err)
		}

		for _, r := range allResults {
			if r.Err != nil {
				stats.Errors++
			} else {
				stats.Processed++
			}
		}

		if time.Since(lastProgress) > 7*time.Second {
			fmt.Printf("progress: %d processed, %d errors\n", stats.Processed, stats.Errors)
			lastProgress = time.Now()
		}
	}

	stats.Duration = time.Since(start)
	return stats, nil
}
