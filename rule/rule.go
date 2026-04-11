// Package rule defines the Rule interface and the hardcoded ordered list of
// rules applied when building a plan.
package rule

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/plan"
)

// Rule evaluates accumulated metadata and proposes file operations.
//
// The claimed set contains file IDs already accounted for by prior rules in
// the pipeline. A rule must skip any file already in the set. BuildPlan
// populates the set with file IDs from each rule's output before invoking the
// next rule.
type Rule interface {
	Name() string
	Evaluate(ctx context.Context, db *sql.DB, cfg *config.Config, claimed map[int64]bool) ([]plan.Op, error)
}

// All returns all registered rules in hardcoded priority order. The first
// rule to claim a file wins.
func All() []Rule {
	return []Rule{
		Junk{},
	}
}

// ByName looks up a rule by its Name().
func ByName(name string) (Rule, bool) {
	for _, r := range All() {
		if r.Name() == name {
			return r, true
		}
	}
	return nil, false
}

// BuildPlan runs the given rules in order and aggregates their proposed ops
// into a single Plan. Each rule sees the set of file IDs claimed by prior
// rules.
func BuildPlan(ctx context.Context, db *sql.DB, cfg *config.Config, rules []Rule) (*plan.Plan, error) {
	claimed := make(map[int64]bool)
	var all []plan.Op
	for _, r := range rules {
		ops, err := r.Evaluate(ctx, db, cfg, claimed)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.Name(), err)
		}
		for _, op := range ops {
			if op.FileID != 0 {
				claimed[op.FileID] = true
			}
		}
		all = append(all, ops...)
	}
	return &plan.Plan{Ops: all}, nil
}
