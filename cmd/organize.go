package cmd

import (
	"fmt"
	"time"

	"github.com/davars/winnow/organize"
	"github.com/spf13/cobra"
)

func newOrganizeCmd() *cobra.Command {
	var (
		removeOriginals bool
		dryRun          bool
	)

	cmd := &cobra.Command{
		Use:   "organize",
		Short: "Copy media from raw to clean/media/YYYY/MM/ based on capture time",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOrganize(cmd, removeOriginals, dryRun)
		},
	}

	cmd.Flags().BoolVar(&removeOriginals, "remove-originals", false, "remove raw originals after successful copy (turns copy into move)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print proposed destinations; make no FS or DB changes")

	return cmd
}

func runOrganize(cmd *cobra.Command, removeOriginals, dryRun bool) error {
	cfg, database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	if _, err := cfg.Location(); err != nil {
		return fmt.Errorf("%w — run `winnow init` to configure it", err)
	}

	stats, err := organize.Run(cmd.Context(), database, cfg, organize.Opts{
		RemoveOriginals: removeOriginals,
		DryRun:          dryRun,
	})

	fmt.Printf("Organize complete: %d organized, %d skipped, %d collided, %d errors (%s)\n",
		stats.Organized, stats.Skipped, stats.Collided, stats.Errors,
		stats.Duration.Round(100*time.Millisecond))

	return err
}
