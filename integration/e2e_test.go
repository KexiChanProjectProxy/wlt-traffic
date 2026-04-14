package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kexi/traffic-count/internal/config"
	"github.com/kexi/traffic-count/internal/storage"
	"github.com/kexi/traffic-count/internal/testutil"
)

func TestWindowQueries(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-windows-*.db")
	if err != nil {
		t.Fatalf("creating temp db: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	today := storage.CurrentDateUTC()

	mac1 := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}
	mac2 := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x02}

	key1 := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac1,
	}
	key2 := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac2,
	}

	delta1 := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   500,
		IngressPackets: 5,
		EgressBytes:    500,
		EgressPackets:  5,
	}
	delta2 := &storage.TrafficCounters{
		Bytes:          2000,
		Packets:        20,
		IngressBytes:   1000,
		IngressPackets: 10,
		EgressBytes:    1000,
		EgressPackets:  10,
	}

	if err := repo.UpsertTotal(ctx, key1, delta1); err != nil {
		t.Fatalf("upserting total: %v", err)
	}
	if err := repo.UpsertTotal(ctx, key2, delta2); err != nil {
		t.Fatalf("upserting total: %v", err)
	}
	if err := repo.UpsertDaily(ctx, key1, today, delta1); err != nil {
		t.Fatalf("upserting daily: %v", err)
	}
	if err := repo.UpsertDaily(ctx, key2, today, delta2); err != nil {
		t.Fatalf("upserting daily: %v", err)
	}

	totals, err := repo.ListTotals(ctx, "")
	if err != nil {
		t.Fatalf("listing totals: %v", err)
	}
	if len(totals) != 2 {
		t.Errorf("expected 2 totals, got %d", len(totals))
	}

	daily, err := repo.ListDaily(ctx, today, today, "")
	if err != nil {
		t.Fatalf("listing daily: %v", err)
	}
	if len(daily) != 2 {
		t.Errorf("expected 2 daily records, got %d", len(daily))
	}
}

func TestRestartPersistence(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-restart-*.db")
	if err != nil {
		t.Fatalf("creating temp db: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	cfg := config.New()
	cfg.DatabasePath = tmpFile.Name()

	ctx := context.Background()
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x03}
	key := &storage.RecordKey{
		InterfaceName: "lo",
		Ifindex:       1,
		MAC:           mac,
	}

	{
		repo, err := storage.New(cfg.DatabasePath)
		if err != nil {
			t.Fatalf("creating repo: %v", err)
		}

		delta := &storage.TrafficCounters{
			Bytes:          5000,
			Packets:        50,
			IngressBytes:   2500,
			IngressPackets: 25,
			EgressBytes:    2500,
			EgressPackets:  25,
		}

		if err := repo.UpsertTotal(ctx, key, delta); err != nil {
			t.Fatalf("upserting total: %v", err)
		}

		repo.Close()
	}

	{
		repo2, err := storage.New(cfg.DatabasePath)
		if err != nil {
			t.Fatalf("reopening repo: %v", err)
		}
		defer repo2.Close()

		total, err := repo2.GetTotal(ctx, key)
		if err != nil {
			t.Fatalf("getting total after restart: %v", err)
		}
		if total == nil {
			t.Fatal("total should exist after restart")
		}
		if total.Bytes != 5000 {
			t.Errorf("expected 5000 bytes, got %d", total.Bytes)
		}
		if total.Packets != 50 {
			t.Errorf("expected 50 packets, got %d", total.Packets)
		}
	}
}

func TestMultiInterfaceDistinctCounting(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-multi-*.db")
	if err != nil {
		t.Fatalf("creating temp db: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	sameMac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	key1 := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           sameMac,
	}
	key2 := &storage.RecordKey{
		InterfaceName: "eth1",
		Ifindex:       3,
		MAC:           sameMac,
	}

	delta := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   500,
		IngressPackets: 5,
		EgressBytes:    500,
		EgressPackets:  5,
	}

	if err := repo.UpsertTotal(ctx, key1, delta); err != nil {
		t.Fatalf("upserting total for eth0: %v", err)
	}
	if err := repo.UpsertTotal(ctx, key2, delta); err != nil {
		t.Fatalf("upserting total for eth1: %v", err)
	}

	totals, err := repo.ListTotals(ctx, "")
	if err != nil {
		t.Fatalf("listing totals: %v", err)
	}
	if len(totals) != 2 {
		t.Errorf("expected 2 distinct records, got %d", len(totals))
	}

	total1, err := repo.GetTotal(ctx, key1)
	if err != nil {
		t.Fatalf("getting total for eth0: %v", err)
	}
	if total1.Bytes != 1000 {
		t.Errorf("expected eth0 to have 1000 bytes, got %d", total1.Bytes)
	}

	total2, err := repo.GetTotal(ctx, key2)
	if err != nil {
		t.Fatalf("getting total for eth1: %v", err)
	}
	if total2.Bytes != 1000 {
		t.Errorf("expected eth1 to have 1000 bytes, got %d", total2.Bytes)
	}
}

func TestTestHarness(t *testing.T) {
	h, err := testutil.NewHarness("lo")
	if err != nil {
		t.Fatalf("creating harness: %v", err)
	}
	defer h.Cleanup()

	tmpDB, err := testutil.NewTempDB()
	if err != nil {
		t.Fatalf("creating temp db: %v", err)
	}
	defer tmpDB.Close()

	t.Logf("Temp DB created at: %s", tmpDB.Path)
}

func TestDailyDateRange(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-daily-*.db")
	if err != nil {
		t.Fatalf("creating temp db: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		t.Fatalf("creating repo: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x05}
	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac,
	}

	yesterday := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -1))
	today := storage.CurrentDateUTC()

	delta := &storage.TrafficCounters{
		Bytes:          100,
		Packets:        1,
		IngressBytes:   50,
		IngressPackets: 1,
		EgressBytes:    50,
		EgressPackets:  0,
	}

	if err := repo.UpsertDaily(ctx, key, yesterday, delta); err != nil {
		t.Fatalf("upserting yesterday: %v", err)
	}
	if err := repo.UpsertDaily(ctx, key, today, delta); err != nil {
		t.Fatalf("upserting today: %v", err)
	}

	weekOld := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -7))
	recent, err := repo.ListDaily(ctx, weekOld, today, "")
	if err != nil {
		t.Fatalf("listing recent: %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("expected 2 recent records, got %d", len(recent))
	}

	oldCutoff := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -30))
	old, err := repo.ListDaily(ctx, oldCutoff, yesterday, "")
	if err != nil {
		t.Fatalf("listing old: %v", err)
	}
	if len(old) != 1 {
		t.Errorf("expected 1 old record, got %d", len(old))
	}
}
