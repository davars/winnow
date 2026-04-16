package cmd

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"github.com/davars/winnow/config"
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

	if err := ensureOrganizeTimezone(cfg); err != nil {
		return err
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

// ensureOrganizeTimezone interactively fills in organize.timezone when it's
// missing and writes the updated config back to disk, so the prompt only
// fires once per installation.
func ensureOrganizeTimezone(cfg *config.Config) error {
	if cfg.Organize.Timezone != "" {
		return nil
	}
	path, err := config.Find(cfgFile)
	if err != nil {
		return err
	}
	fmt.Println("organize.timezone is not set.")
	tz, err := pickTimezone(bufio.NewReader(os.Stdin), os.Stdout)
	if err != nil {
		return fmt.Errorf("prompting for timezone: %w", err)
	}
	cfg.Organize.Timezone = tz
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("saving timezone to %s: %w", path, err)
	}
	fmt.Printf("Saved organize.timezone = %q to %s\n", tz, path)
	return nil
}
