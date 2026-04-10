package cmd

import (
	"fmt"
	"time"

	"github.com/davars/winnow/mime"
	"github.com/davars/winnow/worker"
	"github.com/spf13/cobra"
)

func newMimeCmd() *cobra.Command {
	var workers int

	cmd := &cobra.Command{
		Use:   "mime",
		Short: "Detect MIME types for files (shells out to file/libmagic)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMime(cmd, workers)
		},
	}

	cmd.Flags().IntVar(&workers, "workers", 0, "parallel workers (default: num CPUs)")

	return cmd
}

func runMime(cmd *cobra.Command, workers int) error {
	cfg, database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	stats, err := mime.Run(cmd.Context(), database, cfg.Stores(), worker.Opts{
		Workers: workers,
	})
	if err != nil {
		return err
	}

	fmt.Printf("MIME detection complete: %d files processed, %d errors (%s)\n",
		stats.Processed, stats.Errors, stats.Duration.Round(100*time.Millisecond))

	return nil
}
