package organize

import (
	"path/filepath"
	"strings"
	"time"
)

// destinationPath returns the clean-relative path a file should land at, given
// its capture timestamp (already in UTC), content hash, and original path.
// Layout: media/YYYY/MM/<compactISO>-<shortSHA>-<basename>.<ext>
func destinationPath(captureUTC time.Time, sha256Hex, origPath string) string {
	base, ext := filenameParts(origPath)
	base = sanitizeBasename(base)
	ext = strings.ToLower(ext)

	ts := captureUTC.UTC().Format("20060102T150405Z")
	filename := ts + "-" + shortSHA(sha256Hex) + "-" + base + ext

	year := captureUTC.UTC().Format("2006")
	month := captureUTC.UTC().Format("01")
	return filepath.Join("media", year, month, filename)
}

// filenameParts splits the final path element into (base, ext). For dotfiles
// with no second dot (e.g. ".hidden") the whole name is the base and ext is
// empty. For anything else, ext is everything from the final "." onward
// (including the dot).
func filenameParts(origPath string) (base, ext string) {
	name := filepath.Base(origPath)
	ext = filepath.Ext(name)
	base = strings.TrimSuffix(name, ext)
	if base == "" {
		base = name
		ext = ""
	}
	return base, ext
}

// sanitizeBasename replaces characters that would make the destination filename
// unsafe or confusing: path separators, NUL, other control bytes. Leading and
// trailing whitespace is trimmed first (so pure-whitespace input collapses to
// "_"). Empty result becomes "_" to guarantee a valid filename.
func sanitizeBasename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == 0:
			b.WriteByte('_')
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shortSHA returns the first 8 hex characters of a content hash, or the whole
// string when shorter.
func shortSHA(s string) string {
	if len(s) < 8 {
		return s
	}
	return s[:8]
}
