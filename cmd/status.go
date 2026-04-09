package cmd

import (
	"fmt"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show database statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(verbose)
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show additional details")

	return cmd
}

func runStatus(verbose bool) error {
	path, err := config.Find(cfgFile)
	if err != nil {
		return err
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer database.Close()

	stats, err := db.GetStats(database)
	if err != nil {
		return err
	}

	fmt.Printf("Database: %s\n", cfg.DBPath())
	fmt.Printf("Files:       %d\n", stats.Files)
	fmt.Printf("Directories: %d\n", stats.Directories)
	fmt.Printf("Missing:     %d\n", stats.Missing)
	fmt.Printf("Operations:  %d\n", stats.Operations)
	fmt.Printf("Errors:      %d\n", stats.Errors)

	if verbose {
		fmt.Printf("\nConfig: %s\n", path)
		fmt.Printf("  raw_dir:   %s\n", cfg.RawDir)
		fmt.Printf("  clean_dir: %s\n", cfg.CleanDir)
		fmt.Printf("  trash_dir: %s\n", cfg.TrashDir)
		fmt.Printf("  data_dir:  %s\n", cfg.DataDir)
	}

	return nil
}
