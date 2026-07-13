package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSweepStalePartials verifies the partial-directory garbage collector:
// old directories with no live download state are removed, while active and
// fresh ones are kept. Before this existed, partial assembly files from
// downloads that exhausted their retry window leaked disk forever.
func TestSweepStalePartials(t *testing.T) {
	c, err := New(t.TempDir(), 1<<20, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	mkPartial := func(hash string, age time.Duration) {
		if err := c.EnsurePartialDir(hash); err != nil {
			t.Fatalf("EnsurePartialDir: %v", err)
		}
		dir := c.PartialDir(hash)
		if err := os.WriteFile(filepath.Join(dir, "assembled"), []byte("partial data"), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	mkPartial("stale-abandoned", 48*time.Hour) // old, no state → removed
	mkPartial("stale-active", 48*time.Hour)    // old, still tracked → kept
	mkPartial("fresh", time.Minute)            // recent → kept

	removed, err := c.SweepStalePartials(24*time.Hour, func(hash string) bool {
		return hash == "stale-active"
	})
	if err != nil {
		t.Fatalf("SweepStalePartials: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	if _, err := os.Stat(c.PartialDir("stale-abandoned")); !os.IsNotExist(err) {
		t.Error("abandoned stale partial dir was not removed")
	}
	if _, err := os.Stat(c.PartialDir("stale-active")); err != nil {
		t.Error("active partial dir was removed but must be kept")
	}
	if _, err := os.Stat(c.PartialDir("fresh")); err != nil {
		t.Error("fresh partial dir was removed but must be kept")
	}
}
