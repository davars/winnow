package cmd

import (
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
	if !slices.Contains(got, "America/New_York") || !slices.Contains(got, "America/North_Dakota/New_Salem") {
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
		if !slices.Contains(got, w) {
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
