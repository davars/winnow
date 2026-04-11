package cmd

import (
	"fmt"
	"os"

	"github.com/davars/winnow/plan"
	"github.com/davars/winnow/rule"
	"github.com/spf13/cobra"
)

func newProcessCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "process [rule]",
		Short: "Run rules and execute the proposed operations",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProcess(cmd, args)
		},
	}
}

func runProcess(cmd *cobra.Command, args []string) error {
	cfg, database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	rules, err := selectRules(args)
	if err != nil {
		return err
	}

	p, err := rule.BuildPlan(cmd.Context(), database, cfg, rules)
	if err != nil {
		return err
	}

	p.Print(os.Stdout)

	if len(p.Ops) == 0 {
		return nil
	}

	stats, err := plan.Execute(cmd.Context(), database, p, plan.ExecuteOpts{
		Stores:         cfg.Stores(),
		PreProcessHook: cfg.PreProcessHook,
	})
	fmt.Printf("\nProcess complete: %d succeeded, %d failed\n", stats.Succeeded, stats.Failed)
	return err
}
