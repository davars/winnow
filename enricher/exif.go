package enricher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

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

// exifToolArgs are the invariant flags passed to exiftool for every batch.
// We ask for every tag (no -TagName filter) so the schema adapts to whatever
// the file carries, minus groups that don't describe content: System (fs
// attributes, already in files table), File (container bookkeeping, MIMEType
// is in mime table), and ExifTool (exiftool's own version). -G0 prefixes tag
// names with their family-0 group ("EXIF:CreateDate" vs "QuickTime:CreateDate")
// so same-named tags from different groups don't collapse. -fast2 skips
// MakerNotes (vendor-specific blobs, often hundreds of KB on RAW files).
var exifToolArgs = []string{
	"-json", "-fast2", "-G0",
	"--System:all",
	"--File:all",
	"--ExifTool:all",
}

// binaryPlaceholder matches exiftool's textual stand-in for binary-valued
// tags when -b is not supplied (e.g. "(Binary data 123 bytes, use -b option
// to extract)"). Values matching this pattern are dropped so `data` stays
// text, not opaque placeholders.
var binaryPlaceholder = regexp.MustCompile(`^\(Binary data \d+ bytes`)

// exifTagsVersion is a short hash of the extraction policy (exiftool args +
// binary placeholder pattern). Stored per-row so any policy change triggers
// re-processing on the next Identify pass.
var exifTagsVersion = computeTagsVersion()

func computeTagsVersion() string {
	sorted := slices.Clone(exifToolArgs)
	slices.Sort(sorted)
	h := sha256.New()
	for _, a := range sorted {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	h.Write([]byte(binaryPlaceholder.String()))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// EXIF identifies candidates by MIME type (not extension) so renamed or
// extensionless files are still picked up. Requires the mime enricher to
// have run first. The extracted tags are stored as a JSON object in the
// `data` column, keyed by exiftool's "Group:Tag" name.
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
	// Reset rows whose extraction policy differs from the current version so
	// they get re-processed. Also covers rows created before tags_version
	// existed — after the ALTER TABLE migration they have tags_version IS NULL.
	if _, err := database.ExecContext(ctx, `
		UPDATE exif SET processed_at = NULL
		WHERE processed_at IS NOT NULL
		  AND (tags_version IS NULL OR tags_version != ?)
	`, exifTagsVersion); err != nil {
		return 0, fmt.Errorf("resetting stale tag versions: %w", err)
	}
	return IdentifyByMimeType(ctx, database, e.TableName(), EXIFMimeTypes)
}

// exiftoolResult keeps unknown tags out of the top-level struct by using
// RawMessage per-tag, so SourceFile/Error remain typed while arbitrary tags
// pass through uninterpreted.
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
// Error at the struct level. Binary placeholders and null values are dropped.
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
				if isEmptyOrBinary(v) {
					continue
				}
				r.Tags[k] = v
			}
		}
		results[i] = r
	}
	return results, nil
}

// isEmptyOrBinary reports whether a raw JSON value should be dropped from the
// stored tag map: empty/null, or a string matching exiftool's binary-data
// placeholder.
func isEmptyOrBinary(v json.RawMessage) bool {
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return true
	}
	if v[0] != '"' {
		return false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return false
	}
	return binaryPlaceholder.MatchString(s)
}

// encodeTags marshals the tag map to a JSON object. Keys are sorted for
// deterministic output so identical inputs produce byte-identical data
// (helpful for diffing and cache keys). Returns "{}" when tags is empty so
// `data IS NULL` remains an unambiguous signal of per-row failure.
func encodeTags(tags map[string]json.RawMessage) string {
	if len(tags) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kJSON, _ := json.Marshal(k)
		buf.Write(kJSON)
		buf.WriteByte(':')
		buf.Write(tags[k])
	}
	buf.WriteByte('}')
	return buf.String()
}

func runExiftool(ctx context.Context, items []worker.WorkItem) ([]byte, error) {
	args := make([]string, 0, len(exifToolArgs)+len(items)+1)
	args = append(args, exifToolArgs...)
	args = append(args, "--")
	for _, item := range items {
		args = append(args, item.Path)
	}
	return runTool(ctx, "exiftool", args...)
}
