package storage

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRepository_New(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_storage_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	// Verify WAL mode is set.
	var mode string
	err = repo.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if err != nil {
		t.Fatalf("querying journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestRepository_SchemaInit(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_schema_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	// Verify tables exist.
	tables := []string{"mac_traffic_totals", "mac_traffic_daily", "collector_checkpoints"}
	for _, table := range tables {
		var name string
		err := repo.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}

	// Verify indexes exist.
	indexes := []string{"idx_daily_date_utc", "idx_totals_interface", "idx_daily_interface", "idx_checkpoints_interface"}
	for _, idx := range indexes {
		var name string
		err := repo.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&name)
		if err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}
}

func TestRepository_UpsertDaily_Accumulation(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_upsert_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &RecordKey{
		InterfaceName: "eth0",
		Ifindex:       1,
		MAC:           [6]byte{0xDE, 0xAD, 0xBE, 0xEF, 0xFE, 0xED},
	}
	dateUTC := "2026-04-13"

	// First upsert.
	delta1 := &TrafficCounters{
		Bytes:          100,
		Packets:        10,
		IngressBytes:   60,
		IngressPackets: 6,
		EgressBytes:    40,
		EgressPackets:  4,
	}

	ctx := context.Background()
	if err := repo.UpsertDaily(ctx, key, dateUTC, delta1); err != nil {
		t.Fatalf("UpsertDaily() error = %v", err)
	}

	// Verify first upsert.
	dr, err := repo.GetDaily(ctx, key, dateUTC)
	if err != nil {
		t.Fatalf("GetDaily() error = %v", err)
	}
	if dr == nil {
		t.Fatal("GetDaily() returned nil, want record")
	}
	if dr.Bytes != 100 {
		t.Errorf("Bytes = %d, want 100", dr.Bytes)
	}
	if dr.Packets != 10 {
		t.Errorf("Packets = %d, want 10", dr.Packets)
	}

	// Second upsert with same key/date should accumulate.
	delta2 := &TrafficCounters{
		Bytes:          200,
		Packets:        20,
		IngressBytes:   120,
		IngressPackets: 12,
		EgressBytes:    80,
		EgressPackets:  8,
	}

	if err := repo.UpsertDaily(ctx, key, dateUTC, delta2); err != nil {
		t.Fatalf("UpsertDaily() second call error = %v", err)
	}

	// Verify accumulated values.
	dr, err = repo.GetDaily(ctx, key, dateUTC)
	if err != nil {
		t.Fatalf("GetDaily() error = %v", err)
	}
	if dr.Bytes != 300 {
		t.Errorf("Bytes = %d, want 300", dr.Bytes)
	}
	if dr.Packets != 30 {
		t.Errorf("Packets = %d, want 30", dr.Packets)
	}
	if dr.IngressBytes != 180 {
		t.Errorf("IngressBytes = %d, want 180", dr.IngressBytes)
	}
	if dr.EgressBytes != 120 {
		t.Errorf("EgressBytes = %d, want 120", dr.EgressBytes)
	}
}

func TestRepository_UpsertTotal_Accumulation(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_total_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           [6]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF},
	}

	delta1 := &TrafficCounters{
		Bytes:          1000,
		Packets:        100,
		IngressBytes:   600,
		IngressPackets: 60,
		EgressBytes:    400,
		EgressPackets:  40,
	}

	ctx := context.Background()
	if err := repo.UpsertTotal(ctx, key, delta1); err != nil {
		t.Fatalf("UpsertTotal() error = %v", err)
	}

	tr, err := repo.GetTotal(ctx, key)
	if err != nil {
		t.Fatalf("GetTotal() error = %v", err)
	}
	if tr == nil {
		t.Fatal("GetTotal() returned nil, want record")
	}
	if tr.Bytes != 1000 {
		t.Errorf("Bytes = %d, want 1000", tr.Bytes)
	}

	// Second upsert accumulates.
	delta2 := &TrafficCounters{
		Bytes:          500,
		Packets:        50,
		IngressBytes:   300,
		IngressPackets: 30,
		EgressBytes:    200,
		EgressPackets:  20,
	}

	if err := repo.UpsertTotal(ctx, key, delta2); err != nil {
		t.Fatalf("UpsertTotal() second call error = %v", err)
	}

	tr, err = repo.GetTotal(ctx, key)
	if err != nil {
		t.Fatalf("GetTotal() error = %v", err)
	}
	if tr.Bytes != 1500 {
		t.Errorf("Bytes = %d, want 1500", tr.Bytes)
	}
	if tr.IngressBytes != 900 {
		t.Errorf("IngressBytes = %d, want 900", tr.IngressBytes)
	}
	if tr.EgressBytes != 600 {
		t.Errorf("EgressBytes = %d, want 600", tr.EgressBytes)
	}
}

func TestRepository_PruneDaily(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_prune_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &RecordKey{
		InterfaceName: "eth0",
		Ifindex:       1,
		MAC:           [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66},
	}
	delta := &TrafficCounters{Bytes: 100, Packets: 10}
	ctx := context.Background()

	// Insert records for different dates.
	dates := []string{"2024-01-01", "2025-01-01", "2025-06-01", "2026-01-01", "2026-04-01", "2026-04-13"}
	for _, date := range dates {
		if err := repo.UpsertDaily(ctx, key, date, delta); err != nil {
			t.Fatalf("UpsertDaily(%s) error = %v", date, err)
		}
	}

	// Prune anything before 2026-01-01.
	cutoff := "2026-01-01"
	rowsDeleted, err := repo.PruneDaily(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneDaily() error = %v", err)
	}
	if rowsDeleted != 3 {
		t.Errorf("rowsDeleted = %d, want 3", rowsDeleted)
	}

	// Verify records after cutoff remain.
	for _, date := range []string{"2026-01-01", "2026-04-01", "2026-04-13"} {
		dr, err := repo.GetDaily(ctx, key, date)
		if err != nil {
			t.Fatalf("GetDaily(%s) error = %v", date, err)
		}
		if dr == nil {
			t.Errorf("record for %s was pruned, should exist", date)
		}
	}

	// Verify records before cutoff are gone.
	for _, date := range []string{"2024-01-01", "2025-01-01", "2025-06-01"} {
		dr, err := repo.GetDaily(ctx, key, date)
		if err != nil {
			t.Fatalf("GetDaily(%s) error = %v", date, err)
		}
		if dr != nil {
			t.Errorf("record for %s still exists, should be pruned", date)
		}
	}
}

func TestRepository_ListDaily_DateRange(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_list_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	keys := []*RecordKey{
		{InterfaceName: "eth0", Ifindex: 1, MAC: [6]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11}},
		{InterfaceName: "eth0", Ifindex: 1, MAC: [6]byte{0x22, 0x22, 0x22, 0x22, 0x22, 0x22}},
		{InterfaceName: "eth1", Ifindex: 2, MAC: [6]byte{0x33, 0x33, 0x33, 0x33, 0x33, 0x33}},
	}
	delta := &TrafficCounters{Bytes: 100, Packets: 10}
	ctx := context.Background()

	// Insert records across dates.
	recordDates := []string{"2026-04-10", "2026-04-11", "2026-04-12", "2026-04-13"}
	for _, key := range keys {
		for _, date := range recordDates {
			if err := repo.UpsertDaily(ctx, key, date, delta); err != nil {
				t.Fatalf("UpsertDaily() error = %v", err)
			}
		}
	}

	// List all records for a date range.
	records, err := repo.ListDaily(ctx, "2026-04-11", "2026-04-12", "")
	if err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	// 3 keys * 2 days = 6 records
	if len(records) != 6 {
		t.Errorf("len(records) = %d, want 6", len(records))
	}

	// List with interface filter.
	records, err = repo.ListDaily(ctx, "2026-04-10", "2026-04-13", "eth0")
	if err != nil {
		t.Fatalf("ListDaily(eth0) error = %v", err)
	}
	// 2 keys on eth0 * 4 days = 8 records
	if len(records) != 8 {
		t.Errorf("len(records) = %d, want 8", len(records))
	}

	// List with interface filter for eth1.
	records, err = repo.ListDaily(ctx, "2026-04-10", "2026-04-13", "eth1")
	if err != nil {
		t.Fatalf("ListDaily(eth1) error = %v", err)
	}
	if len(records) != 4 {
		t.Errorf("len(records) = %d, want 4", len(records))
	}
}

func TestRepository_Checkpoint_UpsertAndGet(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_cp_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &RecordKey{
		InterfaceName: "eth0",
		Ifindex:       1,
		MAC:           [6]byte{0xDE, 0xAD, 0xBE, 0xEF, 0xFE, 0xED},
	}
	dateUTC := "2026-04-13"
	raw := &TrafficCounters{
		Bytes:          5000,
		Packets:        500,
		IngressBytes:   3000,
		IngressPackets: 300,
		EgressBytes:    2000,
		EgressPackets:  200,
	}

	ctx := context.Background()

	// Initially no checkpoint.
	cp, err := repo.GetCheckpoint(ctx, key)
	if err != nil {
		t.Fatalf("GetCheckpoint() error = %v", err)
	}
	if cp != nil {
		t.Error("GetCheckpoint() should return nil for new key")
	}

	// Upsert checkpoint.
	if err := repo.UpsertCheckpoint(ctx, key, dateUTC, raw); err != nil {
		t.Fatalf("UpsertCheckpoint() error = %v", err)
	}

	// Get checkpoint and verify.
	cp, err = repo.GetCheckpoint(ctx, key)
	if err != nil {
		t.Fatalf("GetCheckpoint() error = %v", err)
	}
	if cp == nil {
		t.Fatal("GetCheckpoint() returned nil, want checkpoint")
	}
	if cp.DateUTC != dateUTC {
		t.Errorf("DateUTC = %q, want %q", cp.DateUTC, dateUTC)
	}
	if cp.LastRawBytes != 5000 {
		t.Errorf("LastRawBytes = %d, want 5000", cp.LastRawBytes)
	}
	if cp.LastIngressBytes != 3000 {
		t.Errorf("LastIngressBytes = %d, want 3000", cp.LastIngressBytes)
	}
	if cp.LastEgressBytes != 2000 {
		t.Errorf("LastEgressBytes = %d, want 2000", cp.LastEgressBytes)
	}

	// Update checkpoint with new values.
	newRaw := &TrafficCounters{
		Bytes:          6000,
		Packets:        600,
		IngressBytes:   3500,
		IngressPackets: 350,
		EgressBytes:    2500,
		EgressPackets:  250,
	}

	if err := repo.UpsertCheckpoint(ctx, key, dateUTC, newRaw); err != nil {
		t.Fatalf("UpsertCheckpoint() second call error = %v", err)
	}

	cp, err = repo.GetCheckpoint(ctx, key)
	if err != nil {
		t.Fatalf("GetCheckpoint() error = %v", err)
	}
	// Checkpoint should have latest values.
	if cp.LastRawBytes != 6000 {
		t.Errorf("LastRawBytes = %d, want 6000", cp.LastRawBytes)
	}
}

func TestRepository_ListCheckpoints(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_list_cp_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	keys := []*RecordKey{
		{InterfaceName: "eth0", Ifindex: 1, MAC: [6]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11}},
		{InterfaceName: "eth0", Ifindex: 1, MAC: [6]byte{0x22, 0x22, 0x22, 0x22, 0x22, 0x22}},
		{InterfaceName: "eth1", Ifindex: 2, MAC: [6]byte{0x33, 0x33, 0x33, 0x33, 0x33, 0x33}},
	}
	raw := &TrafficCounters{Bytes: 1000, Packets: 100}
	ctx := context.Background()

	for _, key := range keys {
		if err := repo.UpsertCheckpoint(ctx, key, "2026-04-13", raw); err != nil {
			t.Fatalf("UpsertCheckpoint() error = %v", err)
		}
	}

	// List all checkpoints.
	checkpoints, err := repo.ListCheckpoints(ctx, "")
	if err != nil {
		t.Fatalf("ListCheckpoints() error = %v", err)
	}
	if len(checkpoints) != 3 {
		t.Errorf("len(checkpoints) = %d, want 3", len(checkpoints))
	}

	// List with interface filter.
	checkpoints, err = repo.ListCheckpoints(ctx, "eth0")
	if err != nil {
		t.Fatalf("ListCheckpoints(eth0) error = %v", err)
	}
	if len(checkpoints) != 2 {
		t.Errorf("len(checkpoints) = %d, want 2", len(checkpoints))
	}

	checkpoints, err = repo.ListCheckpoints(ctx, "eth1")
	if err != nil {
		t.Fatalf("ListCheckpoints(eth1) error = %v", err)
	}
	if len(checkpoints) != 1 {
		t.Errorf("len(checkpoints) = %d, want 1", len(checkpoints))
	}
}

func TestRepository_ListTotals(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_list_totals_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	keys := []*RecordKey{
		{InterfaceName: "eth0", Ifindex: 1, MAC: [6]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11}},
		{InterfaceName: "eth0", Ifindex: 1, MAC: [6]byte{0x22, 0x22, 0x22, 0x22, 0x22, 0x22}},
		{InterfaceName: "eth1", Ifindex: 2, MAC: [6]byte{0x33, 0x33, 0x33, 0x33, 0x33, 0x33}},
	}
	delta := &TrafficCounters{Bytes: 1000, Packets: 100}
	ctx := context.Background()

	for _, key := range keys {
		if err := repo.UpsertTotal(ctx, key, delta); err != nil {
			t.Fatalf("UpsertTotal() error = %v", err)
		}
	}

	// List all totals.
	totals, err := repo.ListTotals(ctx, "")
	if err != nil {
		t.Fatalf("ListTotals() error = %v", err)
	}
	if len(totals) != 3 {
		t.Errorf("len(totals) = %d, want 3", len(totals))
	}

	// List with interface filter.
	totals, err = repo.ListTotals(ctx, "eth0")
	if err != nil {
		t.Fatalf("ListTotals(eth0) error = %v", err)
	}
	if len(totals) != 2 {
		t.Errorf("len(totals) = %d, want 2", len(totals))
	}
}

func TestCurrentDateUTC(t *testing.T) {
	date := CurrentDateUTC()
	if len(date) != 10 {
		t.Errorf("CurrentDateUTC() = %q, want 10 chars YYYY-MM-DD", date)
	}
}

func TestFormatDateUTC(t *testing.T) {
	// 2026-04-13 12:34:56 UTC
	timestamp := time.Date(2026, 4, 13, 12, 34, 56, 0, time.UTC)
	formatted := FormatDateUTC(timestamp)
	if formatted != "2026-04-13" {
		t.Errorf("FormatDateUTC() = %q, want %q", formatted, "2026-04-13")
	}
}

func TestPruneCutoffDate(t *testing.T) {
	cutoff := PruneCutoffDate()
	if len(cutoff) != 10 {
		t.Errorf("PruneCutoffDate() = %q, want 10 chars YYYY-MM-DD", cutoff)
	}
}

func TestRepository_TotalsNotAffectedByPrune(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test_totals_preserve_*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	repo, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer repo.Close()

	key := &RecordKey{
		InterfaceName: "eth0",
		Ifindex:       1,
		MAC:           [6]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF},
	}
	delta := &TrafficCounters{Bytes: 5000, Packets: 500}
	ctx := context.Background()

	// Upsert total.
	if err := repo.UpsertTotal(ctx, key, delta); err != nil {
		t.Fatalf("UpsertTotal() error = %v", err)
	}

	// Prune with a very old cutoff should not affect totals.
	_, err = repo.PruneDaily(ctx, "2020-01-01")
	if err != nil {
		t.Fatalf("PruneDaily() error = %v", err)
	}

	// Totals should still exist.
	tr, err := repo.GetTotal(ctx, key)
	if err != nil {
		t.Fatalf("GetTotal() error = %v", err)
	}
	if tr == nil {
		t.Fatal("GetTotal() returned nil, total should still exist after prune")
	}
	if tr.Bytes != 5000 {
		t.Errorf("Bytes = %d, want 5000 (totals should not be affected by prune)", tr.Bytes)
	}
}
