package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// zoneinfoRoots are scanned to enumerate valid IANA zones for fuzzy matching.
// Different platforms lay out zoneinfo differently; we try both and merge.
var zoneinfoRoots = []string{
	"/usr/share/zoneinfo",
	"/var/db/timezone/zoneinfo",
}

// detectSystemTZ reads /etc/localtime and extracts the zone name from the path
// it points at (expecting .../zoneinfo/<Region>/<City>). Returns "" if the
// link is missing, not a symlink, or not under a known zoneinfo root.
func detectSystemTZ() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	return extractZoneName(target)
}

// extractZoneName finds the "zoneinfo/" segment in a path and returns what
// follows (the IANA zone name). Empty if no such segment is present.
func extractZoneName(p string) string {
	const needle = "zoneinfo/"
	i := strings.LastIndex(p, needle)
	if i < 0 {
		return ""
	}
	return p[i+len(needle):]
}

// loadZoneList walks known zoneinfo roots and returns a deduplicated list of
// valid IANA zone names (e.g. "America/New_York", "Etc/UTC"). Non-zone entries
// (.tab files, posix/, right/, leapseconds, lowercase top-level files) are
// skipped.
func loadZoneList() ([]string, error) {
	return loadZonesFromRoots(zoneinfoRoots)
}

func loadZonesFromRoots(roots []string) ([]string, error) {
	seen := make(map[string]struct{})
	var found []string

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		// os.Stat follows symlinks so IsDir is true even when root is a link.
		// filepath.WalkDir does not follow symlinks, so the walk would bail at
		// the root. Resolve to the real directory first.
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		err = filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				base := d.Name()
				if path != realRoot && (base == "posix" || base == "right") {
					return fs.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(realRoot, path)
			if err != nil {
				return nil
			}
			if !isZoneCandidate(rel) {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if _, ok := seen[rel]; ok {
				return nil
			}
			seen[rel] = struct{}{}
			found = append(found, rel)
			return nil
		})
		if err != nil {
			return found, err
		}
	}

	sort.Strings(found)
	return found, nil
}

// isZoneCandidate filters filesystem entries under a zoneinfo root to just
// plausible IANA zone names. Reject .tab / leapseconds / tzdata version file,
// plus top-level files that start with a lowercase letter (like
// "zone1970.tab", "iso3166.tab", "tzdata.zi").
func isZoneCandidate(rel string) bool {
	if strings.HasSuffix(rel, ".tab") || strings.HasSuffix(rel, ".zi") ||
		strings.HasSuffix(rel, ".list") {
		return false
	}
	base := filepath.Base(rel)
	if base == "leapseconds" || base == "leap-seconds.list" || base == "tzdata.zi" {
		return false
	}
	// Top-level files starting with a lowercase ASCII letter are metadata,
	// not zones — IANA zones all start with uppercase (or a region prefix
	// like Etc/, America/). Paths with separators are nested (real zones).
	if !strings.ContainsRune(rel, filepath.Separator) {
		if len(base) == 0 {
			return false
		}
		if base[0] >= 'a' && base[0] <= 'z' {
			return false
		}
	}
	return true
}

// matchZones returns zones that match query, ranked: exact case-insensitive
// first, then starts-with (of any token), then contains. Within each rank,
// zones are sorted alphabetically.
func matchZones(query string, zones []string) []string {
	q := normalizeZone(query)
	if q == "" {
		return nil
	}
	tokens := strings.Fields(q)

	var exact, prefix, contains []string
	for _, z := range zones {
		nz := normalizeZone(z)
		allMatch := true
		anyPrefix := false
		for _, t := range tokens {
			if !strings.Contains(nz, t) {
				allMatch = false
				break
			}
			if strings.HasPrefix(nz, t) {
				anyPrefix = true
			}
		}
		if !allMatch {
			continue
		}
		switch {
		case nz == q:
			exact = append(exact, z)
		case anyPrefix:
			prefix = append(prefix, z)
		default:
			contains = append(contains, z)
		}
	}
	sort.Strings(exact)
	sort.Strings(prefix)
	sort.Strings(contains)
	return append(append(exact, prefix...), contains...)
}

// normalizeZone lowercases the string and replaces `_`, `/`, `-` with spaces
// so fuzzy matching is separator-agnostic.
func normalizeZone(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '_', '/', '-':
			b.WriteByte(' ')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.TrimSpace(b.String())
}
