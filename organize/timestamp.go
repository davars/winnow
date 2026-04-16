package organize

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// exifTagPriority is the ordered list of EXIF/JSON tags consulted for capture
// time. The first tag with a parseable value wins. When a naive timestamp is
// found, exifOffsetTag maps it to the companion offset tag (if any).
var exifTagPriority = []string{
	"Composite:SubSecDateTimeOriginal",
	"EXIF:DateTimeOriginal",
	"EXIF:CreateDate",
	"QuickTime:CreateDate",
	"XMP:CreateDate",
	"RIFF:DateTimeOriginal",
}

var exifOffsetTag = map[string]string{
	"EXIF:DateTimeOriginal": "EXIF:OffsetTimeOriginal",
	"EXIF:CreateDate":       "EXIF:OffsetTime",
}

// captureTime returns the best available capture timestamp for a file, the
// tag it was sourced from (for audit), and any parse error. Priority runs
// down exifTagPriority; a fallback to files.mod_time (already ISO UTC) kicks
// in when EXIF data is empty, missing, or un-parseable for every priority tag.
//
// loc is consulted only when an EXIF value is naive AND no matching offset tag
// is present. Never falls back to time.Local.
func captureTime(exifJSON, modTime string, loc *time.Location) (time.Time, string, error) {
	if exifJSON != "" {
		tags := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(exifJSON), &tags); err == nil {
			for _, tag := range exifTagPriority {
				raw, ok := tags[tag]
				if !ok {
					continue
				}
				var s string
				if err := json.Unmarshal(raw, &s); err != nil || s == "" {
					continue
				}
				offset := ""
				if offTag, ok := exifOffsetTag[tag]; ok {
					if off, ok := tags[offTag]; ok {
						_ = json.Unmarshal(off, &offset)
					}
				}
				t, err := parseExifDate(s, offset, loc)
				if err != nil {
					continue
				}
				return t.UTC(), tag, nil
			}
		}
	}

	if modTime != "" {
		t, err := time.Parse(time.RFC3339, modTime)
		if err == nil {
			return t.UTC(), "files.mod_time", nil
		}
	}
	return time.Time{}, "", fmt.Errorf("no usable timestamp (exif + mod_time both unparseable)")
}

// parseExifDate parses an EXIF-style date string. Accepts "YYYY:MM:DD HH:MM:SS"
// with optional ".mmm" subseconds and an optional trailing "Z" or "±HH:MM".
// When the string is naive and offsetHint is non-empty (e.g. "-04:00"), it is
// treated as the correct UTC offset; otherwise loc is used. loc must be
// non-nil for naive values without an offset hint.
func parseExifDate(s, offsetHint string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty date")
	}

	// Detect and strip an embedded offset / Z suffix.
	embeddedOffset, stripped := extractOffset(s)
	s = stripped

	// Split date and time portions so we can be permissive about subseconds.
	// EXIF dates use `YYYY:MM:DD HH:MM:SS[.frac]`.
	layouts := []string{
		"2006:01:02 15:04:05",
		"2006:01:02 15:04:05.000",
		"2006:01:02 15:04:05.00",
		"2006:01:02 15:04:05.0",
		"2006:01:02 15:04:05.000000",
	}

	parseLocation := loc
	if embeddedOffset != "" {
		off, err := parseOffset(embeddedOffset)
		if err != nil {
			return time.Time{}, err
		}
		parseLocation = off
	} else if offsetHint != "" {
		off, err := parseOffset(offsetHint)
		if err == nil {
			parseLocation = off
		}
	}

	if parseLocation == nil {
		return time.Time{}, fmt.Errorf("naive timestamp %q and no location configured", s)
	}

	var firstErr error
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, s, parseLocation)
		if err == nil {
			return t, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return time.Time{}, firstErr
}

// extractOffset pulls an embedded offset ("Z" or "±HH:MM") off the end of an
// EXIF date string, returning the offset token and the remaining naive portion.
func extractOffset(s string) (offset, rest string) {
	if strings.HasSuffix(s, "Z") {
		return "+00:00", strings.TrimSuffix(s, "Z")
	}
	// Look for ±HH:MM in the last 6 chars.
	if len(s) >= 6 {
		tail := s[len(s)-6:]
		if (tail[0] == '+' || tail[0] == '-') && tail[3] == ':' {
			return tail, s[:len(s)-6]
		}
	}
	return "", s
}

// parseOffset converts "+HH:MM" or "-HH:MM" to a *time.Location with a fixed
// offset.
func parseOffset(s string) (*time.Location, error) {
	if len(s) != 6 || s[3] != ':' {
		return nil, fmt.Errorf("invalid offset %q", s)
	}
	sign := 1
	switch s[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return nil, fmt.Errorf("invalid offset sign %q", s)
	}
	var hh, mm int
	if _, err := fmt.Sscanf(s[1:3]+s[4:6], "%02d%02d", &hh, &mm); err != nil {
		return nil, fmt.Errorf("invalid offset %q: %w", s, err)
	}
	seconds := sign * (hh*3600 + mm*60)
	return time.FixedZone(s, seconds), nil
}
