package enricher

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/davars/winnow/db"
	"github.com/davars/winnow/worker"
)

// ImageMimeTypes enumerates the formats exiftool can reliably read metadata
// from. Extending this list is the intended way to cover new RAW formats.
var ImageMimeTypes = []string{
	"image/jpeg",
	"image/heic",
	"image/heif",
	"image/tiff",
	"image/png",
	"image/webp",
	"image/gif",
	"image/x-canon-cr2",
	"image/x-nikon-nef",
	"image/x-sony-arw",
	"image/x-adobe-dng",
}

// EXIF identifies candidates by MIME type (not extension) so renamed or
// extensionless files are still picked up. Requires the mime enricher to
// have run first.
type EXIF struct{}

func (EXIF) Name() string      { return "exif" }
func (EXIF) TableName() string { return "exif" }

func (EXIF) Columns() []db.Column {
	return []db.Column{
		{Name: "create_date", Type: "TEXT"},
		{Name: "make", Type: "TEXT"},
		{Name: "model", Type: "TEXT"},
	}
}

func (EXIF) Indexes() []db.Index { return nil }

func (EXIF) ProcessBatch() int { return 100 }

func (e EXIF) Identify(ctx context.Context, database *sql.DB) (int, error) {
	return IdentifyByMimeType(ctx, database, e.TableName(), ImageMimeTypes)
}

// exiftoolResult tolerates type drift in exiftool's output: some camera models
// cause Make/Model to be emitted as JSON numbers rather than strings.
type exiftoolResult struct {
	SourceFile string          `json:"SourceFile"`
	CreateDate json.RawMessage `json:"CreateDate,omitempty"`
	Make       json.RawMessage `json:"Make,omitempty"`
	Model      json.RawMessage `json:"Model,omitempty"`
	Error      string          `json:"Error,omitempty"`
}

func (e EXIF) Process(ctx context.Context, items []worker.WorkItem) []worker.WorkResult {
	results := make([]worker.WorkResult, len(items))

	// exiftool exits 1 on warnings but still emits valid JSON on stdout; only
	// fail the whole batch if we got nothing back.
	out, runErr := runExiftool(ctx, items)
	if runErr != nil && len(out) == 0 {
		for i, item := range items {
			results[i] = worker.WorkResult{Item: item, Err: runErr}
		}
		return results
	}

	var parsed []exiftoolResult
	if err := json.Unmarshal(out, &parsed); err != nil {
		wrap := fmt.Errorf("parsing exiftool output: %w", err)
		for i, item := range items {
			results[i] = worker.WorkResult{Item: item, Err: wrap}
		}
		return results
	}

	bySource := make(map[string]exiftoolResult, len(parsed))
	for _, r := range parsed {
		bySource[r.SourceFile] = r
	}

	for i, item := range items {
		r, ok := bySource[item.Path]
		if !ok {
			results[i] = worker.WorkResult{
				Item: item,
				Err:  fmt.Errorf("exif: no result for %s", item.Path),
			}
			continue
		}
		if r.Error != "" {
			results[i] = worker.WorkResult{
				Item: item,
				Err:  fmt.Errorf("exif %s: %s", item.Path, r.Error),
			}
			continue
		}
		results[i] = worker.WorkResult{
			Item: item,
			Values: map[string]any{
				"create_date": decodeExifString(r.CreateDate),
				"make":        decodeExifString(r.Make),
				"model":       decodeExifString(r.Model),
			},
		}
	}
	return results
}

// runExiftool uses -fast2 to skip the MakerNotes section — big speedup on
// RAW files, and we only need top-level tags. -s keeps tag names short
// ("CreateDate" not "EXIF:CreateDate") so they match the struct tags.
func runExiftool(ctx context.Context, items []worker.WorkItem) ([]byte, error) {
	args := make([]string, 0, len(items)+7)
	args = append(args, "-json", "-s", "-fast2",
		"-CreateDate", "-Make", "-Model", "--")
	for _, item := range items {
		args = append(args, item.Path)
	}
	return runTool(ctx, "exiftool", args...)
}

// decodeExifString handles exiftool's occasional numeric emission of Make/Model
// by trying string first, then json.Number, then falling back to the raw bytes.
func decodeExifString(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return strings.TrimSpace(string(raw))
}
