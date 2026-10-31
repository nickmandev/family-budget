package main

import (
	"testing"
	"time"
)

// TestTimezonesResolve guards the blank `time/tzdata` import in main.go. On a
// developer machine with system tzdata this passes regardless, but on the
// minimal Alpine runtime image the embedded database is what makes it succeed.
func TestTimezonesResolve(t *testing.T) {
	for _, name := range []string{"UTC", "Europe/Sofia", "America/New_York", "Asia/Tokyo"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("time.LoadLocation(%q) error = %v", name, err)
		}
		if loc == nil {
			t.Fatalf("time.LoadLocation(%q) returned nil", name)
		}
	}

	loc, err := time.LoadLocation("Europe/Sofia")
	if err != nil {
		t.Fatalf("time.LoadLocation(Europe/Sofia) error = %v", err)
	}
	if got := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).In(loc).Format("2006-01-02"); got != "2026-10-30" {
		t.Fatalf("date in Europe/Sofia = %s, want 2026-10-30", got)
	}
}
