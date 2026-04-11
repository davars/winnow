package cmd

import (
	"fmt"
	"os"

	"github.com/davars/winnow/rule"
	"github.com/spf13/cobra"
)

func newPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plan [rule]",
		Short: "Run rules and print the proposed operations (dry-run)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, args)
		},
	}
}

func runPlan(cmd *cobra.Command, args []string) error {
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
	return nil
}

// selectRules resolves the optional rule-name argument to the slice of rules
// BuildPlan should run. No args means every rule in priority order.
func selectRules(args []string) ([]rule.Rule, error) {
	if len(args) == 0 {
		return rule.All(), nil
	}
	r, ok := rule.ByName(args[0])
	if !ok {
		return nil, fmt.Errorf("unknown rule %q", args[0])
	}
	return []rule.Rule{r}, nil
}
