package cmd

import (
	"errors"
	"fmt"

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
	state, database, err := openBootstrapDB()
	if err != nil {
		return err
	}
	defer database.Close()

	stats, err := db.GetStats(database)
	if err != nil {
		return err
	}

	fmt.Printf("Database: %s\n", state.Bootstrap.DBPath())
	fmt.Printf("Files:       %d\n", stats.Files)
	fmt.Printf("Directories: %d\n", stats.Directories)
	fmt.Printf("Missing:     %d\n", stats.Missing)
	fmt.Printf("Operations:  %d\n", stats.Operations)
	fmt.Printf("Errors:      %d\n", stats.Errors)

	if verbose {
		configPath := state.ConfigPath
		if configPath == "" {
			configPath = "(via --data-dir)"
		}
		fmt.Printf("\nConfig: %s\n", configPath)
		fmt.Printf("  data_dir:  %s\n", state.Bootstrap.DataDir)

		settings, err := db.LoadSettings(database)
		if err != nil {
			if errors.Is(err, db.ErrSettingsNotConfigured) {
				fmt.Printf("  settings:  not configured\n")
				return nil
			}
			return err
		}

		fmt.Printf("  raw_dir:   %s\n", settings.RawDir)
		fmt.Printf("  clean_dir: %s\n", settings.CleanDir)
		fmt.Printf("  trash_dir: %s\n", settings.TrashDir)
		fmt.Printf("  hook:      %s\n", settings.PreProcessHook)
		fmt.Printf("  staleness: %s\n", settings.Reconcile.MaxStaleness)
		fmt.Printf("  timezone:  %s\n", settings.Organize.Timezone)
	}

	return nil
}
