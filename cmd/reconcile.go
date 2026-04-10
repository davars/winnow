package cmd

import (
	"fmt"

	"github.com/davars/winnow/reconcile"
	"github.com/spf13/cobra"
)

func newReconcileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile",
		Short: "Mark stale files as missing",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReconcile(cmd)
		},
	}
}

func runReconcile(cmd *cobra.Command) error {
	cfg, database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	maxStaleness, err := cfg.MaxStalenessDuration()
	if err != nil {
		return fmt.Errorf("invalid max_staleness: %w", err)
	}

	stats, err := reconcile.Run(cmd.Context(), database, maxStaleness)
	if err != nil {
		return err
	}

	fmt.Printf("Reconcile complete: %d files marked as missing (staleness threshold: %s)\n",
		stats.Marked, maxStaleness)

	return nil
}
