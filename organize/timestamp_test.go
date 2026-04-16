package organize

import (
	"testing"
	"time"
)

var testLoc = time.FixedZone("test-5", -5*3600)

func TestCaptureTimePriority(t *testing.T) {
	// Composite:SubSecDateTimeOriginal wins over EXIF:DateTimeOriginal.
	exifJSON := `{
		"Composite:SubSecDateTimeOriginal": "2022:06:15 12:00:00.123",
		"EXIF:DateTimeOriginal":            "2022:06:15 12:00:00",
		"EXIF:CreateDate":                  "2022:06:15 12:00:00"
	}`
	tt, src, err := captureTime(exifJSON, "", testLoc)
	if err != nil {
		t.Fatal(err)
	}
	if src != "Composite:SubSecDateTimeOriginal" {
		t.Errorf("source = %q, want Composite:SubSecDateTimeOriginal", src)
	}
	// testLoc is -05:00; parsed naive → UTC shifts +5 hours.
	want := time.Date(2022, 6, 15, 17, 0, 0, 123000000, time.UTC)
	if !tt.Equal(want) {
		t.Errorf("captureTime = %v, want %v", tt, want)
	}
}

func TestCaptureTimeOffsetTagUsedWhenNaive(t *testing.T) {
	// Naive DateTimeOriginal + explicit OffsetTimeOriginal should ignore loc.
	exifJSON := `{
		"EXIF:DateTimeOriginal":   "2011:06:12 16:35:06",
		"EXIF:OffsetTimeOriginal": "-04:00"
	}`
	// Deliberately pass a very different loc to prove it's ignored.
	tt, src, err := captureTime(exifJSON, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if src != "EXIF:DateTimeOriginal" {
		t.Errorf("source = %q, want EXIF:DateTimeOriginal", src)
	}
	want := time.Date(2011, 6, 12, 20, 35, 6, 0, time.UTC)
	if !tt.Equal(want) {
		t.Errorf("captureTime = %v, want %v", tt, want)
	}
}

func TestCaptureTimeEmbeddedOffset(t *testing.T) {
	exifJSON := `{"EXIF:DateTimeOriginal": "2011:06:12 16:35:06-04:00"}`
	// loc should be ignored when value carries its own offset.
	tt, _, err := captureTime(exifJSON, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2011, 6, 12, 20, 35, 6, 0, time.UTC)
	if !tt.Equal(want) {
		t.Errorf("captureTime = %v, want %v", tt, want)
	}
}

func TestCaptureTimeNaiveUsesConfigTZ(t *testing.T) {
	exifJSON := `{"EXIF:DateTimeOriginal": "2020:03:01 09:00:00"}`
	// testLoc is -05:00; 09:00 local → 14:00 UTC.
	tt, _, err := captureTime(exifJSON, "", testLoc)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2020, 3, 1, 14, 0, 0, 0, time.UTC)
	if !tt.Equal(want) {
		t.Errorf("captureTime = %v, want %v", tt, want)
	}
}

func TestCaptureTimeSubsecondPreserved(t *testing.T) {
	exifJSON := `{"Composite:SubSecDateTimeOriginal": "2022:06:15 12:00:00.456Z"}`
	tt, _, err := captureTime(exifJSON, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if tt.Nanosecond() != 456_000_000 {
		t.Errorf("Nanosecond = %d, want 456000000", tt.Nanosecond())
	}
}

func TestCaptureTimeFallsBackToModTime(t *testing.T) {
	tests := []struct {
		name, exif string
	}{
		{"empty", ""},
		{"null", `{"EXIF:DateTimeOriginal": null}`},
		{"unparseable", "not json"},
		{"priority tags absent", `{"Other:Tag": "2020:01:01 00:00:00"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt, src, err := captureTime(tc.exif, "2021-04-05T06:07:08Z", testLoc)
			if err != nil {
				t.Fatal(err)
			}
			if src != "files.mod_time" {
				t.Errorf("source = %q, want files.mod_time", src)
			}
			want := time.Date(2021, 4, 5, 6, 7, 8, 0, time.UTC)
			if !tt.Equal(want) {
				t.Errorf("captureTime = %v, want %v", tt, want)
			}
		})
	}
}

func TestCaptureTimeQuickTime(t *testing.T) {
	// QuickTime:CreateDate is typically UTC ("...Z" appended by exiftool with -api QuickTimeUTC).
	// If no Z or offset is present, it's treated as naive → use loc.
	exifJSON := `{"QuickTime:CreateDate": "2023:08:15 10:20:30Z"}`
	tt, src, err := captureTime(exifJSON, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if src != "QuickTime:CreateDate" {
		t.Errorf("source = %q", src)
	}
	want := time.Date(2023, 8, 15, 10, 20, 30, 0, time.UTC)
	if !tt.Equal(want) {
		t.Errorf("captureTime = %v, want %v", tt, want)
	}
}

func TestCaptureTimeRIFF(t *testing.T) {
	exifJSON := `{"RIFF:DateTimeOriginal": "2019:12:31 23:59:59"}`
	tt, src, err := captureTime(exifJSON, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if src != "RIFF:DateTimeOriginal" {
		t.Errorf("source = %q", src)
	}
	want := time.Date(2019, 12, 31, 23, 59, 59, 0, time.UTC)
	if !tt.Equal(want) {
		t.Errorf("captureTime = %v, want %v", tt, want)
	}
}

func TestCaptureTimeNoUsableTimestamp(t *testing.T) {
	_, _, err := captureTime("", "", time.UTC)
	if err == nil {
		t.Fatal("expected error with empty exif + empty mod_time")
	}
}

func TestParseExifDate(t *testing.T) {
	tests := []struct {
		in, offset string
		wantUTC    time.Time
	}{
		{"2018:10:30 21:17:30Z", "", time.Date(2018, 10, 30, 21, 17, 30, 0, time.UTC)},
		{"2018:10:30 21:17:30.500Z", "", time.Date(2018, 10, 30, 21, 17, 30, 500_000_000, time.UTC)},
		{"2011:06:12 16:35:06-04:00", "", time.Date(2011, 6, 12, 20, 35, 6, 0, time.UTC)},
		{"2020:03:01 09:00:00", "-04:00", time.Date(2020, 3, 1, 13, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		got, err := parseExifDate(tc.in, tc.offset, time.UTC)
		if err != nil {
			t.Errorf("parseExifDate(%q, %q): %v", tc.in, tc.offset, err)
			continue
		}
		if !got.UTC().Equal(tc.wantUTC) {
			t.Errorf("parseExifDate(%q, %q) = %v, want %v", tc.in, tc.offset, got.UTC(), tc.wantUTC)
		}
	}
}
