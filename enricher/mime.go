package enricher

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/davars/winnow/db"
	"github.com/davars/winnow/worker"
)

type Mime struct{}

func (Mime) Name() string      { return "mime" }
func (Mime) TableName() string { return "mime" }

func (Mime) Columns() []db.Column {
	return []db.Column{{Name: "mime_type", Type: "TEXT"}}
}

func (Mime) Indexes() []db.Index {
	return []db.Index{
		{Name: "mime_mime_type", Table: "mime", Columns: []string{"mime_type"}},
	}
}

func (Mime) ProcessBatch() int { return 64 }

func (m Mime) Identify(ctx context.Context, database *sql.DB) (int, error) {
	return IdentifyAllHashes(ctx, database, m.TableName())
}

// Process detects MIME types for a batch in a single `file` invocation.
// `file --brief` emits exactly one line per input path, in order.
func (Mime) Process(ctx context.Context, items []worker.WorkItem) []worker.WorkResult {
	results := make([]worker.WorkResult, len(items))

	lines, err := detectMimeBatch(ctx, items)
	if err != nil {
		for i, item := range items {
			results[i] = worker.WorkResult{Item: item, Err: err}
		}
		return results
	}
	if len(lines) != len(items) {
		err := fmt.Errorf("file returned %d lines for %d inputs", len(lines), len(items))
		for i, item := range items {
			results[i] = worker.WorkResult{Item: item, Err: err}
		}
		return results
	}

	for i, item := range items {
		mimeType := strings.TrimSpace(lines[i])
		// `file` exits 0 even when it cannot read the input (e.g., "regular file,
		// no read permission"). Valid MIME types always contain a slash, so that's
		// our signal to treat the line as an error.
		if !strings.Contains(mimeType, "/") {
			results[i] = worker.WorkResult{
				Item: item,
				Err:  fmt.Errorf("detecting mime for %s: %s", item.Path, mimeType),
			}
			continue
		}
		results[i] = worker.WorkResult{
			Item:   item,
			Values: map[string]any{"mime_type": mimeType},
		}
	}
	return results
}

func detectMimeBatch(ctx context.Context, items []worker.WorkItem) ([]string, error) {
	args := make([]string, 0, len(items)+3)
	args = append(args, "--mime-type", "--brief", "--")
	for _, item := range items {
		args = append(args, item.Path)
	}
	out, err := runTool(ctx, "file", args...)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), nil
}
