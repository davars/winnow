package enricher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/davars/winnow/db"
	"github.com/davars/winnow/worker"
)

// EXIFMimeTypes enumerates the formats exiftool can reliably read metadata
// from, covering both still images and videos. Extending this list is the
// intended way to cover new formats.
var EXIFMimeTypes = []string{
	// Still images
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

	// Video
	"video/mp4",
	"video/quicktime",
	"video/x-msvideo",
	"video/x-matroska",
	"video/webm",
	"video/x-m4v",
	"video/mpeg",
	"video/3gpp",
}

// exifTags lists the tag names requested from exiftool and stored in the
// `data` JSON blob. Order here controls the JSON key order in the output.
var exifTags = []string{
	"CreateDate",
	"DateTimeOriginal",
	"SubSecDateTimeOriginal",
	"SubSecCreateDate",
	"Compression",
}

// exifTagsVersion is a short hash of the sorted tag list. Stored alongside
// each processed row so changing exifTags (add, remove, rename) triggers
// re-processing on the next Identify run.
var exifTagsVersion = computeTagsVersion(exifTags)

func computeTagsVersion(tags []string) string {
	sorted := slices.Clone(tags)
	slices.Sort(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(sum[:])[:12]
}

// EXIF identifies candidates by MIME type (not extension) so renamed or
// extensionless files are still picked up. Requires the mime enricher to
// have run first. The extracted tags are stored as a JSON object in the
// `data` column.
type EXIF struct{}

func (EXIF) Name() string      { return "exif" }
func (EXIF) TableName() string { return "exif" }

func (EXIF) Columns() []db.Column {
	return []db.Column{
		{Name: "data", Type: "TEXT"},
		{Name: "tags_version", Type: "TEXT"},
	}
}

func (EXIF) Indexes() []db.Index { return nil }

func (EXIF) ProcessBatch() int { return 100 }

func (e EXIF) Identify(ctx context.Context, database *sql.DB) (int, error) {
	// Reset rows whose tag set differs from the current version so they get
	// re-processed. Also covers rows created before tags_version existed —
	// after the ALTER TABLE migration they have tags_version IS NULL.
	if _, err := database.ExecContext(ctx, `
		UPDATE exif SET processed_at = NULL
		WHERE processed_at IS NOT NULL
		  AND (tags_version IS NULL OR tags_version != ?)
	`, exifTagsVersion); err != nil {
		return 0, fmt.Errorf("resetting stale tag versions: %w", err)
	}
	return IdentifyByMimeType(ctx, database, e.TableName(), EXIFMimeTypes)
}

// exiftoolResult keeps unknown tags out by using RawMessage per-tag, so
// `omitempty` can drop fields exiftool didn't emit rather than storing them
// as JSON null.
type exiftoolResult struct {
	SourceFile string                     `json:"SourceFile"`
	Error      string                     `json:"Error,omitempty"`
	Tags       map[string]json.RawMessage `json:"-"`
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

	parsed, err := parseExiftoolBatch(out)
	if err != nil {
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
				"data":         encodeTags(r.Tags),
				"tags_version": exifTagsVersion,
			},
		}
	}
	return results
}

// parseExiftoolBatch decodes exiftool's top-level JSON array and flattens each
// per-file object into exiftoolResult.Tags while preserving SourceFile and
// Error at the struct level.
func parseExiftoolBatch(out []byte) ([]exiftoolResult, error) {
	var raws []map[string]json.RawMessage
	if err := json.Unmarshal(out, &raws); err != nil {
		return nil, err
	}
	results := make([]exiftoolResult, len(raws))
	for i, raw := range raws {
		r := exiftoolResult{Tags: make(map[string]json.RawMessage, len(raw))}
		for k, v := range raw {
			switch k {
			case "SourceFile":
				_ = json.Unmarshal(v, &r.SourceFile)
			case "Error":
				_ = json.Unmarshal(v, &r.Error)
			default:
				r.Tags[k] = v
			}
		}
		results[i] = r
	}
	return results, nil
}

// encodeTags returns a JSON object containing only the requested EXIF tags
// that were present in the exiftool output. Returns "{}" when no tags matched
// so `data IS NULL` remains an unambiguous signal of per-row failure.
func encodeTags(tags map[string]json.RawMessage) string {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, k := range exifTags {
		v, ok := tags[k]
		if !ok || len(v) == 0 || bytes.Equal(v, []byte("null")) {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		keyJSON, _ := json.Marshal(k)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.String()
}

// runExiftool uses -fast2 to skip the MakerNotes section — big speedup on
// RAW files, and we only need top-level tags. -s keeps tag names short
// ("CreateDate" not "EXIF:CreateDate") so they match the requested keys.
func runExiftool(ctx context.Context, items []worker.WorkItem) ([]byte, error) {
	args := make([]string, 0, len(items)+len(exifTags)+4)
	args = append(args, "-json", "-s", "-fast2")
	for _, tag := range exifTags {
		args = append(args, "-"+tag)
	}
	args = append(args, "--")
	for _, item := range items {
		args = append(args, item.Path)
	}
	return runTool(ctx, "exiftool", args...)
}
