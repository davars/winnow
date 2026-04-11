package cmd

import (
	"fmt"
	"time"

	"github.com/davars/winnow/enricher"
	"github.com/davars/winnow/worker"
	"github.com/spf13/cobra"
)

func newExifCmd() *cobra.Command {
	var (
		workers      int
		identifyOnly bool
	)

	cmd := &cobra.Command{
		Use:   "exif",
		Short: "Extract EXIF metadata from images (shells out to exiftool)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExif(cmd, workers, identifyOnly)
		},
	}

	cmd.Flags().IntVar(&workers, "workers", 0, "parallel workers (default: num CPUs)")
	cmd.Flags().BoolVar(&identifyOnly, "identify", false, "run only the identify pass (populate candidates)")

	return cmd
}

func runExif(cmd *cobra.Command, workers int, identifyOnly bool) error {
	cfg, database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	e := enricher.EXIF{}

	if identifyOnly {
		n, err := enricher.RunIdentify(cmd.Context(), database, e)
		if err != nil {
			return err
		}
		fmt.Printf("EXIF identify complete: %d new candidates\n", n)
		return nil
	}

	identified, stats, err := enricher.Run(cmd.Context(), database, e, cfg.Stores(), worker.Opts{
		Workers: workers,
	})
	if err != nil {
		return err
	}

	fmt.Printf("EXIF complete: %d new candidates identified, %d processed, %d errors (%s)\n",
		identified, stats.Processed, stats.Errors, stats.Duration.Round(100*time.Millisecond))

	return nil
}
