package db

import (
	"path/filepath"
	"testing"
)

func TestHumanFunctionsSQL(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cases := []struct {
		expr string
		want any
	}{
		{"human_bytes(0)", "0 B"},
		{"human_bytes(9)", "9 B"},
		{"human_bytes(1500)", "1.5 kB"},
		{"human_bytes(82854982)", "83 MB"},
		{"human_bytes(NULL)", nil},
		{"human_ibytes(0)", "0 B"},
		{"human_ibytes(1024)", "1.0 KiB"},
		{"human_ibytes(1536)", "1.5 KiB"},
		{"human_ibytes(82854982)", "79 MiB"},
		{"human_ibytes(NULL)", nil},
	}
	for _, c := range cases {
		var got any
		if err := database.QueryRow("SELECT " + c.expr).Scan(&got); err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestHumanNegativeRejected(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var got any
	if err := database.QueryRow("SELECT human_bytes(-1)").Scan(&got); err == nil {
		t.Errorf("expected error for negative input, got %v", got)
	}
}
