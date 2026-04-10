package cmd

import (
	"github.com/spf13/cobra"
)

func newEnrichCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enrich",
		Short: "Run enrichment steps",
	}

	cmd.AddCommand(newWalkCmd())

	return cmd
}
