package organize

import (
	"strings"
	"testing"
	"time"
)

func TestDestinationPathCompactISO(t *testing.T) {
	ts := time.Date(2018, 10, 30, 21, 17, 30, 0, time.UTC)
	got := destinationPath(ts, "a1b2c3d4e5f6", "photos/DSC_0123.JPG")
	want := "media/2018/10/20181030T211730Z-a1b2c3d4-DSC_0123.jpg"
	if got != want {
		t.Errorf("destinationPath = %q, want %q", got, want)
	}
}

func TestDestinationPathNonUTCNormalized(t *testing.T) {
	// Input isn't UTC; we should still emit UTC in the filename.
	offset := time.FixedZone("x", -5*3600)
	ts := time.Date(2020, 6, 1, 20, 0, 0, 0, offset) // 01:00 UTC next day
	got := destinationPath(ts, "abcdef01", "x.png")
	want := "media/2020/06/20200602T010000Z-abcdef01-x.png"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFilenameParts(t *testing.T) {
	tests := []struct {
		in       string
		wantBase string
		wantExt  string
	}{
		{"DSC.JPG", "DSC", ".JPG"},
		{"photos/DSC.JPG", "DSC", ".JPG"},
		{"README", "README", ""},
		{".hidden.jpg", ".hidden", ".jpg"},
		{"no_ext.", "no_ext", "."},
	}
	for _, tc := range tests {
		gotBase, gotExt := filenameParts(tc.in)
		if gotBase != tc.wantBase || gotExt != tc.wantExt {
			t.Errorf("filenameParts(%q) = (%q, %q), want (%q, %q)",
				tc.in, gotBase, gotExt, tc.wantBase, tc.wantExt)
		}
	}
}

func TestFilenamePartsDotfileOnly(t *testing.T) {
	base, ext := filenameParts(".hidden")
	if base != ".hidden" || ext != "" {
		t.Errorf("filenameParts(\".hidden\") = (%q, %q), want (.hidden, \"\")", base, ext)
	}
}

func TestSanitizeBasename(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"ok name", "ok name"},
		{"has/slash", "has_slash"},
		{"has\\back", "has_back"},
		{"with\x00nul", "with_nul"},
		{"ctrl\x01char", "ctrl_char"},
		{"  trimmed  ", "trimmed"},
		{"\t\n", "_"},
	}
	for _, tc := range tests {
		if got := sanitizeBasename(tc.in); got != tc.want {
			t.Errorf("sanitizeBasename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeBasenameEmptyIsUnderscore(t *testing.T) {
	if got := sanitizeBasename(""); got != "_" {
		t.Errorf("sanitizeBasename(\"\") = %q, want _", got)
	}
}

func TestShortSHAHandlesShortStrings(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"abcdef0123456789", "abcdef01"},
		{"abcd", "abcd"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := shortSHA(tc.in); got != tc.want {
			t.Errorf("shortSHA(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDestinationPathPreservesPathlessName(t *testing.T) {
	// filepath.Base strips directories — confirm destination has no directory
	// leakage from the source path other than the generated media/YYYY/MM.
	ts := time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC)
	got := destinationPath(ts, "f00dbeef", "a/b/c/movie.MP4")
	if !strings.HasSuffix(got, "-movie.mp4") {
		t.Errorf("got %q, want suffix -movie.mp4", got)
	}
	if strings.Contains(got, "a/b/c") {
		t.Errorf("got %q, leaked original directory", got)
	}
}
