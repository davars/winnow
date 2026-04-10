package cmd

import (
	"fmt"
	"time"

	"github.com/davars/winnow/sha256"
	"github.com/davars/winnow/worker"
	"github.com/spf13/cobra"
)

func newSHA256Cmd() *cobra.Command {
	var workers int

	cmd := &cobra.Command{
		Use:   "sha256",
		Short: "Compute SHA-256 hashes for files missing them",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSHA256(cmd, workers)
		},
	}

	cmd.Flags().IntVar(&workers, "workers", 0, "parallel workers (default: num CPUs)")

	return cmd
}

func runSHA256(cmd *cobra.Command, workers int) error {
	cfg, database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	stats, err := sha256.Run(cmd.Context(), database, cfg.Stores(), worker.Opts{
		Workers:      workers,
		ProcessBatch: 1,
	})
	if err != nil {
		return err
	}

	fmt.Printf("SHA-256 complete: %d files hashed, %d errors (%s)\n",
		stats.Processed, stats.Errors, stats.Duration.Round(100*time.Millisecond))

	return nil
}
