package cmd

import (
	"fmt"

	"github.com/davars/winnow/walk"
	"github.com/spf13/cobra"
)

func newWalkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "walk",
		Short: "Walk all stores and populate file/directory tables",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWalk(cmd)
		},
	}
}

func runWalk(cmd *cobra.Command) error {
	cfg, database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	stats, err := walk.Run(cmd.Context(), database, cfg)
	if err != nil {
		return err
	}

	fmt.Printf("Walk complete: %d files found, %d directories updated, %d stale directories removed\n",
		stats.FilesFound, stats.DirsUpserted, stats.DirsDeleted)

	return nil
}
