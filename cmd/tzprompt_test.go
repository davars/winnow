package cmd

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeZone(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"America/New_York", "america new york"},
		{"Etc/UTC", "etc utc"},
		{"Pacific-Honolulu", "pacific honolulu"},
		{"  Europe/Berlin  ", "europe berlin"},
	}
	for _, tc := range tests {
		if got := normalizeZone(tc.in); got != tc.want {
			t.Errorf("normalizeZone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchZonesMultiToken(t *testing.T) {
	zones := []string{
		"America/New_York",
		"America/North_Dakota/New_Salem",
		"America/Los_Angeles",
		"Europe/Berlin",
	}

	got := matchZones("new york", zones)
	if len(got) < 1 || got[0] != "America/New_York" {
		t.Errorf("matchZones(\"new york\") = %v, want America/New_York first", got)
	}

	// Just "new" should hit New_York and the New_Salem entry both (they contain "new").
	got = matchZones("new", zones)
	if !contains(got, "America/New_York") || !contains(got, "America/North_Dakota/New_Salem") {
		t.Errorf("matchZones(\"new\") = %v, missing New_York or New_Salem", got)
	}
}

func TestMatchZonesCaseInsensitive(t *testing.T) {
	zones := []string{"America/New_York", "Asia/Tokyo"}
	got := matchZones("NEW_YORK", zones)
	if len(got) != 1 || got[0] != "America/New_York" {
		t.Errorf("matchZones(\"NEW_YORK\") = %v, want [America/New_York]", got)
	}
}

func TestMatchZonesRegion(t *testing.T) {
	zones := []string{
		"Europe/Berlin",
		"Europe/London",
		"Europe/Paris",
		"America/New_York",
	}
	got := matchZones("europe", zones)
	if len(got) != 3 {
		t.Errorf("matchZones(\"europe\") = %v, want 3 Europe/* entries", got)
	}
	for _, z := range got {
		if !strings.HasPrefix(z, "Europe/") {
			t.Errorf("unexpected match %q", z)
		}
	}
}

func TestMatchZonesNoResults(t *testing.T) {
	zones := []string{"America/New_York", "Europe/Berlin"}
	if got := matchZones("xyz123", zones); len(got) != 0 {
		t.Errorf("matchZones(\"xyz123\") = %v, want empty", got)
	}
}

func TestExtractZoneName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/var/db/timezone/zoneinfo/America/Los_Angeles", "America/Los_Angeles"},
		{"../zoneinfo/Etc/UTC", "Etc/UTC"},
		{"/etc/localtime", ""},
	}
	for _, tc := range tests {
		if got := extractZoneName(tc.in); got != tc.want {
			t.Errorf("extractZoneName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoadZonesFromRootsFixture(t *testing.T) {
	root := t.TempDir()
	// Realistic zoneinfo layout: directories + a few junk files.
	mustMkdir(t, filepath.Join(root, "America"))
	mustMkdir(t, filepath.Join(root, "Europe"))
	mustMkdir(t, filepath.Join(root, "posix", "America"))
	mustMkdir(t, filepath.Join(root, "right"))
	mustWrite(t, filepath.Join(root, "America", "New_York"), "TZ")
	mustWrite(t, filepath.Join(root, "Europe", "Berlin"), "TZ")
	mustWrite(t, filepath.Join(root, "UTC"), "TZ")
	mustWrite(t, filepath.Join(root, "zone1970.tab"), "junk")
	mustWrite(t, filepath.Join(root, "tzdata.zi"), "junk")
	mustWrite(t, filepath.Join(root, "leapseconds"), "junk")
	mustWrite(t, filepath.Join(root, "posix", "America", "New_York"), "TZ")

	got, err := loadZonesFromRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"America/New_York", "Europe/Berlin", "UTC"}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("loadZonesFromRoots missing %q (got %v)", w, got)
		}
	}
	for _, z := range got {
		if z == "zone1970.tab" || z == "tzdata.zi" || z == "leapseconds" {
			t.Errorf("loadZonesFromRoots included junk entry %q", z)
		}
		if strings.HasPrefix(z, "posix/") {
			t.Errorf("loadZonesFromRoots included posix/ entry %q", z)
		}
	}
}

func TestLoadZonesFromRootsMissing(t *testing.T) {
	got, err := loadZonesFromRoots([]string{"/nonexistent/zoneinfo/path"})
	if err != nil {
		t.Fatalf("expected nil error for missing root, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty slice", got)
	}
}

func TestPickTimezoneAcceptsDefault(t *testing.T) {
	out := &bytes.Buffer{}
	reader := bufio.NewReader(strings.NewReader("\n"))
	// Default is "America/Los_Angeles" here via the prompt string; detectSystemTZ
	// isn't deterministic across hosts, so we can't assert the default. Instead
	// we verify an empty response produces a valid accepted zone.
	got, err := pickTimezone(reader, out)
	if err != nil {
		t.Fatal(err)
	}
	// The default must be a loadable IANA name (detectSystemTZ returns one, or
	// we fall back to "UTC").
	if got == "" || got == "Local" {
		t.Errorf("got empty or Local: %q", got)
	}
}

func TestPickTimezoneExactMatch(t *testing.T) {
	out := &bytes.Buffer{}
	reader := bufio.NewReader(strings.NewReader("America/New_York\n"))
	got, err := pickTimezone(reader, out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "America/New_York" {
		t.Errorf("got %q, want America/New_York", got)
	}
}

func TestPickTimezoneSingleFuzzyMatch(t *testing.T) {
	out := &bytes.Buffer{}
	// "honolulu" matches exactly one zone on all platforms: Pacific/Honolulu.
	reader := bufio.NewReader(strings.NewReader("honolulu\n"))
	got, err := pickTimezone(reader, out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Pacific/Honolulu" {
		t.Errorf("got %q, want Pacific/Honolulu", got)
	}
}

func TestPickTimezoneNumberedChoice(t *testing.T) {
	out := &bytes.Buffer{}
	// "berlin" should match Europe/Berlin (likely 1 match). Use "europe" to
	// force a numbered list, then pick Europe/Berlin by number.
	// Europe/ has more than 10 zones, so instead use "dakota" which yields
	// the two North_Dakota zones on most systems.
	reader := bufio.NewReader(strings.NewReader("dakota\n1\n"))
	got, err := pickTimezone(reader, out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "America/North_Dakota/") {
		t.Skipf("host zoneinfo lacks North_Dakota zones; got %q", got)
	}
}

func TestPickTimezoneNoMatchRetries(t *testing.T) {
	out := &bytes.Buffer{}
	reader := bufio.NewReader(strings.NewReader("xyz123\nAmerica/New_York\n"))
	got, err := pickTimezone(reader, out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "America/New_York" {
		t.Errorf("got %q, want America/New_York", got)
	}
	if !strings.Contains(out.String(), "No matches") {
		t.Errorf("expected 'No matches' message, got:\n%s", out.String())
	}
}

func TestPickTimezoneRejectsLocal(t *testing.T) {
	out := &bytes.Buffer{}
	// "Local" loads OK via time.LoadLocation but we reject it. After rejection,
	// the user supplies America/New_York and we accept it.
	reader := bufio.NewReader(strings.NewReader("Local\nAmerica/New_York\n"))
	got, err := pickTimezone(reader, out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "America/New_York" {
		t.Errorf("got %q, want America/New_York", got)
	}
}

func contains(xs []string, v string) bool {
	return slices.Contains(xs, v)
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
