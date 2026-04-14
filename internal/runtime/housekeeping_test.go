package runtime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kexi/traffic-count/internal/storage"
)

func TestHousekeepingLoop_PruneDailyOldRows(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_housekeeping_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       1,
		MAC:           [6]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF},
	}
	delta := &storage.TrafficCounters{Bytes: 100, Packets: 10}
	ctx := context.Background()

	oldDate := "2024-01-01"
	if err := repo.UpsertDaily(ctx, key, oldDate, delta); err != nil {
		t.Fatalf("UpsertDaily(%s) error = %v", oldDate, err)
	}
	recentDate := "2026-04-01"
	if err := repo.UpsertDaily(ctx, key, recentDate, delta); err != nil {
		t.Fatalf("UpsertDaily(%s) error = %v", recentDate, err)
	}
	todayDate := storage.CurrentDateUTC()
	if err := repo.UpsertDaily(ctx, key, todayDate, delta); err != nil {
		t.Fatalf("UpsertDaily(%s) error = %v", todayDate, err)
	}

	hk := NewHousekeepingLoop(repo, 3600)
	if err := hk.runOnce(ctx); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}

	cutoff := hk.LastPruneCutoff()
	if cutoff == "" {
		t.Error("LastPruneCutoff() returned empty, expected a date")
	}

	drOld, err := repo.GetDaily(ctx, key, oldDate)
	if err != nil {
		t.Fatalf("GetDaily(%s) error = %v", oldDate, err)
	}
	if drOld != nil {
		t.Errorf("record for %s still exists after pruning, should be gone", oldDate)
	}

	drRecent, err := repo.GetDaily(ctx, key, recentDate)
	if err != nil {
		t.Fatalf("GetDaily(%s) error = %v", recentDate, err)
	}
	if drRecent == nil {
		t.Errorf("record for %s was incorrectly pruned, should exist", recentDate)
	}

	drToday, err := repo.GetDaily(ctx, key, todayDate)
	if err != nil {
		t.Fatalf("GetDaily(%s) error = %v", todayDate, err)
	}
	if drToday == nil {
		t.Errorf("record for today %s was incorrectly pruned, should exist", todayDate)
	}
}

func TestHousekeepingLoop_TotalsNotPruned(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_housekeeping_totals_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       1,
		MAC:           [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66},
	}
	delta := &storage.TrafficCounters{Bytes: 5000, Packets: 500}
	ctx := context.Background()

	if err := repo.UpsertTotal(ctx, key, delta); err != nil {
		t.Fatalf("UpsertTotal() error = %v", err)
	}

	oldDate := "2020-01-01"
	if err := repo.UpsertDaily(ctx, key, oldDate, delta); err != nil {
		t.Fatalf("UpsertDaily(%s) error = %v", oldDate, err)
	}

	hk := NewHousekeepingLoop(repo, 3600)
	if err := hk.runOnce(ctx); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}

	tr, err := repo.GetTotal(ctx, key)
	if err != nil {
		t.Fatalf("GetTotal() error = %v", err)
	}
	if tr == nil {
		t.Fatal("GetTotal() returned nil, totals should exist after pruning")
	}
	if tr.Bytes != 5000 {
		t.Errorf("Bytes = %d, want 5000 (totals must not be affected by pruning)", tr.Bytes)
	}
}

func TestHousekeepingLoop_ManyOldRowsPruned(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_housekeeping_many_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       1,
		MAC:           [6]byte{0xDE, 0xAD, 0xBE, 0xEF, 0xFE, 0xED},
	}
	delta := &storage.TrafficCounters{Bytes: 100, Packets: 10}
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		date := time.Date(2020, 1, i+1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		if err := repo.UpsertDaily(ctx, key, date, delta); err != nil {
			t.Fatalf("UpsertDaily(%s) error = %v", date, err)
		}
	}

	recentDate := "2026-04-01"
	if err := repo.UpsertDaily(ctx, key, recentDate, delta); err != nil {
		t.Fatalf("UpsertDaily(%s) error = %v", recentDate, err)
	}

	hk := NewHousekeepingLoop(repo, 3600)
	if err := hk.runOnce(ctx); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}

	cutoff := hk.LastPruneCutoff()
	t.Logf("Prune cutoff: %s", cutoff)

	drRecent, err := repo.GetDaily(ctx, key, recentDate)
	if err != nil {
		t.Fatalf("GetDaily(%s) error = %v", recentDate, err)
	}
	if drRecent == nil {
		t.Error("recent date record should still exist after pruning")
	}

	dayBeforeCutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for dayBeforeCutoff.Before(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) {
		dateStr := dayBeforeCutoff.Format("2006-01-02")
		dr, err := repo.GetDaily(ctx, key, dateStr)
		if err != nil {
			t.Fatalf("GetDaily(%s) error = %v", dateStr, err)
		}
		if dr != nil {
			t.Errorf("record for %s (before cutoff %s) should have been pruned", dateStr, cutoff)
		}
		dayBeforeCutoff = dayBeforeCutoff.AddDate(0, 0, 1)
	}
}

func TestHousekeepingLoop_StartStop(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_hk_startstop_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	hk := NewHousekeepingLoop(repo, 1)

	ctx := context.Background()
	if err := hk.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if err := hk.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if hk.LastRun().IsZero() {
		t.Error("LastRun() should not be zero after Start")
	}
}

func TestHousekeepingLoop_PruneErrorHandled(t *testing.T) {
	hk := NewHousekeepingLoop(nil, 3600)
	ctx := context.Background()

	if err := hk.runOnce(ctx); err != nil {
		t.Fatalf("runOnce() with nil repo should not error, got: %v", err)
	}

	if hk.PruneError() != nil {
		t.Error("PruneError() should be nil when repo is nil")
	}
}
