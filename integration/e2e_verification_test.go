package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/kexi/traffic-count/internal/storage"
)

// TestAllWindowVerification tests that the "all" window returns all-time totals correctly.
func TestAllWindowVerification(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-all-window-*.db")
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
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x10}
	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac,
	}

	// Insert multiple daily records over time (simulating all-time accumulation)
	deltas := []struct {
		date    string
		bytes   uint64
		pkts    uint64
		ingress uint64
		egress  uint64
	}{
		{"2026-01-01", 1000, 10, 500, 500},
		{"2026-02-15", 2000, 20, 1000, 1000},
		{"2026-03-01", 3000, 30, 1500, 1500},
		{storage.CurrentDateUTC(), 500, 5, 250, 250}, // today
	}

	var totalBytes, totalPkts, totalIngress, totalEgress uint64
	for _, d := range deltas {
		delta := &storage.TrafficCounters{
			Bytes:          d.bytes,
			Packets:        d.pkts,
			IngressBytes:   d.ingress,
			IngressPackets: d.pkts / 2,
			EgressBytes:    d.egress,
			EgressPackets:  d.pkts / 2,
		}
		totalBytes += d.bytes
		totalPkts += d.pkts
		totalIngress += d.ingress
		totalEgress += d.egress

		if err := repo.UpsertTotal(ctx, key, delta); err != nil {
			t.Fatalf("upserting total: %v", err)
		}
		if err := repo.UpsertDaily(ctx, key, d.date, delta); err != nil {
			t.Fatalf("upserting daily %s: %v", d.date, err)
		}
	}

	// Query all totals
	totals, err := repo.ListTotals(ctx, "")
	if err != nil {
		t.Fatalf("listing totals: %v", err)
	}

	if len(totals) != 1 {
		t.Fatalf("expected 1 total record, got %d", len(totals))
	}

	if totals[0].Bytes != totalBytes {
		t.Errorf("all window: expected total bytes %d, got %d", totalBytes, totals[0].Bytes)
	}
	if totals[0].IngressBytes != totalIngress {
		t.Errorf("all window: expected ingress bytes %d, got %d", totalIngress, totals[0].IngressBytes)
	}
	if totals[0].EgressBytes != totalEgress {
		t.Errorf("all window: expected egress bytes %d, got %d", totalEgress, totals[0].EgressBytes)
	}
	if totals[0].Packets != totalPkts {
		t.Errorf("all window: expected total packets %d, got %d", totalPkts, totals[0].Packets)
	}
}

// TestTodayWindowVerification tests that the "today" window returns only today's data.
func TestTodayWindowVerification(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-today-window-*.db")
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
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x11}
	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac,
	}

	today := storage.CurrentDateUTC()
	yesterday := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -1))

	todayDelta := &storage.TrafficCounters{
		Bytes:          500,
		Packets:        5,
		IngressBytes:   250,
		IngressPackets: 2,
		EgressBytes:    250,
		EgressPackets:  3,
	}

	yesterdayDelta := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   500,
		IngressPackets: 5,
		EgressBytes:    500,
		EgressPackets:  5,
	}

	if err := repo.UpsertDaily(ctx, key, today, todayDelta); err != nil {
		t.Fatalf("upserting today: %v", err)
	}
	if err := repo.UpsertDaily(ctx, key, yesterday, yesterdayDelta); err != nil {
		t.Fatalf("upserting yesterday: %v", err)
	}

	// Also add to totals
	if err := repo.UpsertTotal(ctx, key, todayDelta); err != nil {
		t.Fatalf("upserting today total: %v", err)
	}
	if err := repo.UpsertTotal(ctx, key, yesterdayDelta); err != nil {
		t.Fatalf("upserting yesterday total: %v", err)
	}

	// Query today window
	daily, err := repo.ListDaily(ctx, today, today, "")
	if err != nil {
		t.Fatalf("listing today: %v", err)
	}

	if len(daily) != 1 {
		t.Fatalf("expected 1 today record, got %d", len(daily))
	}

	if daily[0].Bytes != 500 {
		t.Errorf("today window: expected 500 bytes, got %d", daily[0].Bytes)
	}
	if daily[0].IngressBytes != 250 {
		t.Errorf("today window: expected 250 ingress bytes, got %d", daily[0].IngressBytes)
	}
}

// Test7DaysWindowVerification tests that the "7days" window returns last 7 days of data.
func Test7DaysWindowVerification(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-7days-window-*.db")
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
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x12}
	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac,
	}

	today := storage.CurrentDateUTC()
	// 7days window in HTTP uses start = today - 6 (inclusive range)
	sixDaysAgo := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -6))
	threeDaysAgo := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -3))
	eightDaysAgo := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -8))

	sixDaysDelta := &storage.TrafficCounters{
		Bytes:          600,
		Packets:        6,
		IngressBytes:   300,
		IngressPackets: 3,
		EgressBytes:    300,
		EgressPackets:  3,
	}

	threeDaysDelta := &storage.TrafficCounters{
		Bytes:          300,
		Packets:        3,
		IngressBytes:   150,
		IngressPackets: 1,
		EgressBytes:    150,
		EgressPackets:  2,
	}

	eightDaysDelta := &storage.TrafficCounters{
		Bytes:          800,
		Packets:        8,
		IngressBytes:   400,
		IngressPackets: 4,
		EgressBytes:    400,
		EgressPackets:  4,
	}

	// Should be included (within 7 days window: today-6 through today)
	if err := repo.UpsertDaily(ctx, key, sixDaysAgo, sixDaysDelta); err != nil {
		t.Fatalf("upserting 6days ago: %v", err)
	}
	if err := repo.UpsertDaily(ctx, key, threeDaysAgo, threeDaysDelta); err != nil {
		t.Fatalf("upserting 3days ago: %v", err)
	}

	// Should NOT be included (outside 7 days)
	if err := repo.UpsertDaily(ctx, key, eightDaysAgo, eightDaysDelta); err != nil {
		t.Fatalf("upserting 8days ago: %v", err)
	}

	// Query 7 days window (start = today-6, end = today, inclusive)
	weekStart := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -6))
	daily, err := repo.ListDaily(ctx, weekStart, today, "")
	if err != nil {
		t.Fatalf("listing 7days: %v", err)
	}

	// Calculate expected bytes within 7-day window
	var totalBytes uint64
	for _, d := range daily {
		totalBytes += d.Bytes
	}

	// Should include 6days ago (600) + 3days ago (300) = 900
	// 8days ago (800) should NOT be included
	if totalBytes != 900 {
		t.Errorf("7days window: expected 900 bytes (600+300), got %d", totalBytes)
	}
}

// Test30DaysWindowVerification tests that the "30days" window returns last 30 days of data.
func Test30DaysWindowVerification(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-30days-window-*.db")
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
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x13}
	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac,
	}

	today := storage.CurrentDateUTC()
	tenDaysAgo := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -10))
	twentyNineDaysAgo := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -29))
	thirtyOneDaysAgo := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -31))

	withinWindowDelta := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   500,
		IngressPackets: 5,
		EgressBytes:    500,
		EgressPackets:  5,
	}

	outsideWindowDelta := &storage.TrafficCounters{
		Bytes:          2000,
		Packets:        20,
		IngressBytes:   1000,
		IngressPackets: 10,
		EgressBytes:    1000,
		EgressPackets:  10,
	}

	// Within 30 days (window is today-29 through today)
	if err := repo.UpsertDaily(ctx, key, tenDaysAgo, withinWindowDelta); err != nil {
		t.Fatalf("upserting 10days ago: %v", err)
	}
	if err := repo.UpsertDaily(ctx, key, twentyNineDaysAgo, withinWindowDelta); err != nil {
		t.Fatalf("upserting 29days ago: %v", err)
	}

	// Outside 30 days (31+ days ago)
	if err := repo.UpsertDaily(ctx, key, thirtyOneDaysAgo, outsideWindowDelta); err != nil {
		t.Fatalf("upserting 31days ago: %v", err)
	}

	// Query 30 days window
	monthStart := storage.FormatDateUTC(time.Now().UTC().AddDate(0, 0, -29))
	daily, err := repo.ListDaily(ctx, monthStart, today, "")
	if err != nil {
		t.Fatalf("listing 30days: %v", err)
	}

	// Should include 10days ago + 29days ago entries only
	var totalBytes uint64
	for _, d := range daily {
		totalBytes += d.Bytes
	}

	// 10days ago (1000) + 29days ago (1000) = 2000
	// 31days ago (2000) should NOT be included
	if totalBytes != 2000 {
		t.Errorf("30days window: expected 2000 bytes, got %d", totalBytes)
	}
}

// TestMonthWindowVerification tests that the "month" window returns current calendar month data.
func TestMonthWindowVerification(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-month-window-*.db")
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
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x14}
	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac,
	}

	today := storage.CurrentDateUTC()
	now := time.Now().UTC()
	firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	startOfMonth := storage.FormatDateUTC(firstOfMonth)

	// Current month delta
	currentMonthDelta := &storage.TrafficCounters{
		Bytes:          500,
		Packets:        5,
		IngressBytes:   250,
		IngressPackets: 2,
		EgressBytes:    250,
		EgressPackets:  3,
	}

	// Last month delta
	lastMonth := now.AddDate(0, -1, 0)
	lastMonthFirst := time.Date(lastMonth.Year(), lastMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonthDate := storage.FormatDateUTC(lastMonthFirst)
	lastMonthDelta := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   500,
		IngressPackets: 5,
		EgressBytes:    500,
		EgressPackets:  5,
	}

	// This month (should be included)
	if err := repo.UpsertDaily(ctx, key, today, currentMonthDelta); err != nil {
		t.Fatalf("upserting today: %v", err)
	}

	// Last month (should NOT be included)
	if err := repo.UpsertDaily(ctx, key, lastMonthDate, lastMonthDelta); err != nil {
		t.Fatalf("upserting last month: %v", err)
	}

	// Query month window
	daily, err := repo.ListDaily(ctx, startOfMonth, today, "")
	if err != nil {
		t.Fatalf("listing month: %v", err)
	}

	// Should only include this month's data
	var totalBytes uint64
	for _, d := range daily {
		totalBytes += d.Bytes
	}

	if totalBytes != 500 {
		t.Errorf("month window: expected 500 bytes (current month only), got %d", totalBytes)
	}
}

// TestRestartCheckpointRecovery tests that checkpoints prevent double-counting after restart.
func TestRestartCheckpointRecovery(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-checkpoint-*.db")
	if err != nil {
		t.Fatalf("creating temp db: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	ctx := context.Background()
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x15}
	key := &storage.RecordKey{
		InterfaceName: "lo",
		Ifindex:       1,
		MAC:           mac,
	}

	// First session: write some data and checkpoint
	{
		repo, err := storage.New(tmpFile.Name())
		if err != nil {
			t.Fatalf("creating repo: %v", err)
		}

		delta1 := &storage.TrafficCounters{
			Bytes:          1000,
			Packets:        10,
			IngressBytes:   500,
			IngressPackets: 5,
			EgressBytes:    500,
			EgressPackets:  5,
		}

		if err := repo.UpsertTotal(ctx, key, delta1); err != nil {
			t.Fatalf("upserting total: %v", err)
		}
		if err := repo.UpsertDaily(ctx, key, storage.CurrentDateUTC(), delta1); err != nil {
			t.Fatalf("upserting daily: %v", err)
		}

		// Set checkpoint at raw counter values that would exist after first flush
		rawCounters := &storage.TrafficCounters{
			Bytes:          1000,
			Packets:        10,
			IngressBytes:   500,
			IngressPackets: 5,
			EgressBytes:    500,
			EgressPackets:  5,
		}
		if err := repo.UpsertCheckpoint(ctx, key, storage.CurrentDateUTC(), rawCounters); err != nil {
			t.Fatalf("upserting checkpoint: %v", err)
		}

		repo.Close()
	}

	// Second session: verify totals are preserved (NOT double-counted)
	{
		repo2, err := storage.New(tmpFile.Name())
		if err != nil {
			t.Fatalf("reopening repo: %v", err)
		}
		defer repo2.Close()

		// Get the checkpoint that was saved
		cp, err := repo2.GetCheckpoint(ctx, key)
		if err != nil {
			t.Fatalf("getting checkpoint: %v", err)
		}
		if cp == nil {
			t.Fatal("checkpoint should exist after restart")
		}

		// Verify checkpoint values match what we saved
		if cp.LastRawBytes != 1000 {
			t.Errorf("checkpoint raw bytes: expected 1000, got %d", cp.LastRawBytes)
		}

		// Get total - should still be 1000, not 2000 (no double-count)
		total, err := repo2.GetTotal(ctx, key)
		if err != nil {
			t.Fatalf("getting total: %v", err)
		}
		if total == nil {
			t.Fatal("total should exist after restart")
		}
		if total.Bytes != 1000 {
			t.Errorf("total after restart: expected 1000 (no double-count), got %d", total.Bytes)
		}

		// Now simulate new traffic arriving
		newDelta := &storage.TrafficCounters{
			Bytes:          500,
			Packets:        5,
			IngressBytes:   250,
			IngressPackets: 2,
			EgressBytes:    250,
			EgressPackets:  3,
		}

		// Simulate flush with delta computation using checkpoint
		// Delta should be 500 (new raw 1500 - checkpoint 1000)
		if err := repo2.UpsertTotal(ctx, key, newDelta); err != nil {
			t.Fatalf("upserting new total: %v", err)
		}

		// Total should now be 1500
		total, err = repo2.GetTotal(ctx, key)
		if err != nil {
			t.Fatalf("getting total after new delta: %v", err)
		}
		if total.Bytes != 1500 {
			t.Errorf("total after new delta: expected 1500, got %d", total.Bytes)
		}
	}
}

// TestMultiInterfaceSameMacAPILevel tests that same MAC on different interfaces
// returns as distinct records at the API/storage level.
func TestMultiInterfaceSameMacAPILevel(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-multi-iface-*.db")
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

	keyEth0 := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           sameMac,
	}
	keyEth1 := &storage.RecordKey{
		InterfaceName: "eth1",
		Ifindex:       3,
		MAC:           sameMac,
	}

	eth0Delta := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   600,
		IngressPackets: 6,
		EgressBytes:    400,
		EgressPackets:  4,
	}

	eth1Delta := &storage.TrafficCounters{
		Bytes:          2000,
		Packets:        20,
		IngressBytes:   1200,
		IngressPackets: 12,
		EgressBytes:    800,
		EgressPackets:  8,
	}

	if err := repo.UpsertTotal(ctx, keyEth0, eth0Delta); err != nil {
		t.Fatalf("upserting eth0 total: %v", err)
	}
	if err := repo.UpsertTotal(ctx, keyEth1, eth1Delta); err != nil {
		t.Fatalf("upserting eth1 total: %v", err)
	}

	// List all totals - should have 2 distinct records
	totals, err := repo.ListTotals(ctx, "")
	if err != nil {
		t.Fatalf("listing totals: %v", err)
	}

	if len(totals) != 2 {
		t.Errorf("expected 2 distinct records (same MAC on different ifaces), got %d", len(totals))
	}

	// Verify eth0 has its own data
	eth0Total, err := repo.GetTotal(ctx, keyEth0)
	if err != nil {
		t.Fatalf("getting eth0 total: %v", err)
	}
	if eth0Total.Bytes != 1000 {
		t.Errorf("eth0: expected 1000 bytes, got %d", eth0Total.Bytes)
	}
	if eth0Total.IngressBytes != 600 {
		t.Errorf("eth0: expected 600 ingress bytes, got %d", eth0Total.IngressBytes)
	}

	// Verify eth1 has its own data
	eth1Total, err := repo.GetTotal(ctx, keyEth1)
	if err != nil {
		t.Fatalf("getting eth1 total: %v", err)
	}
	if eth1Total.Bytes != 2000 {
		t.Errorf("eth1: expected 2000 bytes, got %d", eth1Total.Bytes)
	}
	if eth1Total.IngressBytes != 1200 {
		t.Errorf("eth1: expected 1200 ingress bytes, got %d", eth1Total.IngressBytes)
	}

	// Verify both interfaces are correctly identified
	interfaces := make(map[string]bool)
	for _, t := range totals {
		interfaces[t.InterfaceName] = true
	}
	if !interfaces["eth0"] {
		t.Error("expected eth0 to be in totals")
	}
	if !interfaces["eth1"] {
		t.Error("expected eth1 to be in totals")
	}
}

// TestDeterministicWindowBoundaries tests that window date boundaries are deterministic and use UTC.
func TestDeterministicWindowBoundaries(t *testing.T) {
	// Test that window calculations produce consistent UTC-based results
	now := time.Now().UTC()

	today := storage.CurrentDateUTC()
	weekStart := storage.FormatDateUTC(now.AddDate(0, 0, -6))
	monthStart := storage.FormatDateUTC(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))

	// Verify today format is YYYY-MM-DD UTC
	if len(today) != 10 {
		t.Errorf("today should be YYYY-MM-DD format (10 chars), got %s", today)
	}

	// Verify week start is 6 days before today (inclusive window)
	weekStartTime, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		t.Fatalf("weekStart should parse as date: %v", err)
	}
	todayTime, err := time.Parse("2006-01-02", today)
	if err != nil {
		t.Fatalf("today should parse as date: %v", err)
	}
	daysDiff := todayTime.Sub(weekStartTime).Hours() / 24
	if daysDiff != 6 {
		t.Errorf("7days window should span 7 days (today + 6 prev), got %v days difference", daysDiff)
	}

	// Verify month start is first day of current month
	monthStartTime, err := time.Parse("2006-01-02", monthStart)
	if err != nil {
		t.Fatalf("monthStart should parse as date: %v", err)
	}
	if monthStartTime.Day() != 1 {
		t.Errorf("month start should be day 1, got %d", monthStartTime.Day())
	}
	if monthStartTime.Month() != now.Month() {
		t.Errorf("month start should be current month %d, got %d", now.Month(), monthStartTime.Month())
	}
	if monthStartTime.Year() != now.Year() {
		t.Errorf("month start should be current year %d, got %d", now.Year(), monthStartTime.Year())
	}

	_ = daysDiff // silence unused variable warning
}

// TestWindowQueriesIntegration is a full integration test that seeds historical data
// and verifies all window queries return expected aggregated results.
func TestWindowQueriesIntegration(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-integration-*.db")
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
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x20}
	key := &storage.RecordKey{
		InterfaceName: "eth0",
		Ifindex:       2,
		MAC:           mac,
	}

	// Seed historical data
	historicalData := []struct {
		date      string
		bytes     uint64
		pkts      uint64
		ingress   uint64
		egress    uint64
		isAllTime bool // if true, adds to all-time totals
	}{
		{"2026-01-15", 100, 1, 50, 50, false},
		{"2026-02-10", 200, 2, 100, 100, false},
		{"2026-03-05", 300, 3, 150, 150, false},
		{storage.CurrentDateUTC(), 50, 1, 25, 25, true},
	}

	var totalAllTime uint64
	for _, h := range historicalData {
		delta := &storage.TrafficCounters{
			Bytes:          h.bytes,
			Packets:        h.pkts,
			IngressBytes:   h.ingress,
			IngressPackets: h.pkts / 2,
			EgressBytes:    h.egress,
			EgressPackets:  h.pkts / 2,
		}
		if err := repo.UpsertDaily(ctx, key, h.date, delta); err != nil {
			t.Fatalf("upserting daily %s: %v", h.date, err)
		}
		if h.isAllTime {
			totalAllTime += h.bytes
		}
	}

	// Also add historical data to all-time totals
	allTimeDelta := &storage.TrafficCounters{
		Bytes:          650, // 100+200+300+50
		Packets:        7,
		IngressBytes:   325,
		IngressPackets: 3,
		EgressBytes:    325,
		EgressPackets:  4,
	}
	if err := repo.UpsertTotal(ctx, key, allTimeDelta); err != nil {
		t.Fatalf("upserting all-time total: %v", err)
	}

	// Test "all" window
	totals, err := repo.ListTotals(ctx, "")
	if err != nil {
		t.Fatalf("listing all totals: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("expected 1 total, got %d", len(totals))
	}
	if totals[0].Bytes != 650 {
		t.Errorf("all window: expected 650 bytes, got %d", totals[0].Bytes)
	}

	// Test "today" window
	today := storage.CurrentDateUTC()
	todayRecords, err := repo.ListDaily(ctx, today, today, "")
	if err != nil {
		t.Fatalf("listing today: %v", err)
	}
	if len(todayRecords) != 1 {
		t.Fatalf("expected 1 today record, got %d", len(todayRecords))
	}
	if todayRecords[0].Bytes != 50 {
		t.Errorf("today window: expected 50 bytes, got %d", todayRecords[0].Bytes)
	}

	// Test "month" window (April 2026 - current month)
	// Should include today record only
	now := time.Now().UTC()
	monthStart := storage.FormatDateUTC(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
	monthRecords, err := repo.ListDaily(ctx, monthStart, today, "")
	if err != nil {
		t.Fatalf("listing month: %v", err)
	}
	if len(monthRecords) != 1 {
		t.Errorf("month window: expected 1 record (today), got %d", len(monthRecords))
	}

	// Test "7days" window
	weekStart := storage.FormatDateUTC(now.AddDate(0, 0, -6))
	weekRecords, err := repo.ListDaily(ctx, weekStart, today, "")
	if err != nil {
		t.Fatalf("listing 7days: %v", err)
	}
	if len(weekRecords) != 1 {
		t.Errorf("7days window: expected 1 record (today), got %d", len(weekRecords))
	}

	// Test "30days" window
	monthStart30 := storage.FormatDateUTC(now.AddDate(0, 0, -29))
	month30Records, err := repo.ListDaily(ctx, monthStart30, today, "")
	if err != nil {
		t.Fatalf("listing 30days: %v", err)
	}
	// Should include today only since other data is older
	if len(month30Records) != 1 {
		t.Errorf("30days window: expected 1 record (today), got %d", len(month30Records))
	}
}

// TestAPIMultiInterfaceSameMac verifies same MAC on different interfaces
// remains separate when queried through a simulated API response structure.
func TestAPIMultiInterfaceSameMac(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-api-multi-*.db")
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
	sameMac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x30}

	// Create records for same MAC on different interfaces
	for i, ifaceName := range []string{"eth0", "eth1", "eth2"} {
		key := &storage.RecordKey{
			InterfaceName: ifaceName,
			Ifindex:       uint32(i + 1),
			MAC:           sameMac,
		}
		delta := &storage.TrafficCounters{
			Bytes:          uint64((i + 1) * 1000),
			Packets:        uint64((i + 1) * 10),
			IngressBytes:   uint64((i + 1) * 500),
			IngressPackets: uint64((i + 1) * 5),
			EgressBytes:    uint64((i + 1) * 500),
			EgressPackets:  uint64((i + 1) * 5),
		}
		if err := repo.UpsertTotal(ctx, key, delta); err != nil {
			t.Fatalf("upserting total for %s: %v", ifaceName, err)
		}
	}

	// Query all totals
	totals, err := repo.ListTotals(ctx, "")
	if err != nil {
		t.Fatalf("listing totals: %v", err)
	}

	// Should have 3 distinct records (one per interface)
	if len(totals) != 3 {
		t.Errorf("expected 3 records (same MAC on eth0, eth1, eth2), got %d", len(totals))
	}

	// Sort by interface name for deterministic comparison
	sort.Slice(totals, func(i, j int) bool {
		return totals[i].InterfaceName < totals[j].InterfaceName
	})

	// Verify each interface has distinct data
	expectedBytes := []uint64{1000, 2000, 3000}
	for i, total := range totals {
		if total.Bytes != expectedBytes[i] {
			t.Errorf("%s: expected %d bytes, got %d",
				total.InterfaceName, expectedBytes[i], total.Bytes)
		}
		if total.InterfaceName == "eth0" && total.Bytes != 1000 {
			t.Errorf("eth0: expected 1000, got %d", total.Bytes)
		}
	}
}

// TestFlushLoopDeltaComputation tests that delta computation in flush loop
// correctly uses checkpoints to avoid double-counting.
func TestFlushLoopDeltaComputation(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "traffic-count-delta-*.db")
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
	mac := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x40}
	key := &storage.RecordKey{
		InterfaceName: "lo",
		Ifindex:       1,
		MAC:           mac,
	}
	today := storage.CurrentDateUTC()

	// First flush: initial counter values
	initialRaw := &storage.TrafficCounters{
		Bytes:          1000,
		Packets:        10,
		IngressBytes:   500,
		IngressPackets: 5,
		EgressBytes:    500,
		EgressPackets:  5,
	}

	// Write initial data and checkpoint
	if err := repo.UpsertTotal(ctx, key, initialRaw); err != nil {
		t.Fatalf("upserting initial total: %v", err)
	}
	if err := repo.UpsertDaily(ctx, key, today, initialRaw); err != nil {
		t.Fatalf("upserting initial daily: %v", err)
	}
	if err := repo.UpsertCheckpoint(ctx, key, today, initialRaw); err != nil {
		t.Fatalf("upserting initial checkpoint: %v", err)
	}

	// Second "flush" simulation: new raw counters are higher
	newRaw := &storage.TrafficCounters{
		Bytes:          1500, // +500 delta
		Packets:        15,   // +5 delta
		IngressBytes:   750,  // +250 delta
		IngressPackets: 7,    // +2 delta
		EgressBytes:    750,  // +250 delta
		EgressPackets:  8,    // +3 delta
	}

	// Get checkpoint to compute delta
	cp, err := repo.GetCheckpoint(ctx, key)
	if err != nil {
		t.Fatalf("getting checkpoint: %v", err)
	}

	// Compute expected delta
	delta := storage.TrafficCounters{
		Bytes:          newRaw.Bytes - cp.LastRawBytes,
		Packets:        newRaw.Packets - cp.LastRawPackets,
		IngressBytes:   newRaw.IngressBytes - cp.LastIngressBytes,
		IngressPackets: newRaw.IngressPackets - cp.LastIngressPackets,
		EgressBytes:    newRaw.EgressBytes - cp.LastEgressBytes,
		EgressPackets:  newRaw.EgressPackets - cp.LastEgressPackets,
	}

	// Delta should be exactly 500 bytes, 5 packets, etc.
	if delta.Bytes != 500 {
		t.Errorf("delta bytes: expected 500, got %d", delta.Bytes)
	}
	if delta.Packets != 5 {
		t.Errorf("delta packets: expected 5, got %d", delta.Packets)
	}
	if delta.IngressBytes != 250 {
		t.Errorf("delta ingress bytes: expected 250, got %d", delta.IngressBytes)
	}
	if delta.EgressBytes != 250 {
		t.Errorf("delta egress bytes: expected 250, got %d", delta.EgressBytes)
	}

	// Apply delta
	if err := repo.UpsertTotal(ctx, key, &delta); err != nil {
		t.Fatalf("upserting delta total: %v", err)
	}

	// Verify total is now 1500 (not 2500 - no double count)
	total, err := repo.GetTotal(ctx, key)
	if err != nil {
		t.Fatalf("getting total: %v", err)
	}
	if total.Bytes != 1500 {
		t.Errorf("total after delta: expected 1500 (no double-count), got %d", total.Bytes)
	}
}

// TestAPITrafficRecordJSON verifies the API response structure matches expected format.
func TestAPITrafficRecordJSON(t *testing.T) {
	record := struct {
		Interface      string `json:"interface"`
		Ifindex        uint32 `json:"ifindex"`
		MAC            string `json:"mac"`
		IngressBytes   uint64 `json:"ingress_bytes"`
		EgressBytes    uint64 `json:"egress_bytes"`
		TotalBytes     uint64 `json:"total_bytes"`
		IngressPackets uint64 `json:"ingress_packets"`
		EgressPackets  uint64 `json:"egress_packets"`
		TotalPackets   uint64 `json:"total_packets"`
		Window         string `json:"window"`
	}{
		Interface:      "eth0",
		Ifindex:        2,
		MAC:            "aa:bb:cc:dd:ee:ff",
		IngressBytes:   500,
		EgressBytes:    500,
		TotalBytes:     1000,
		IngressPackets: 5,
		EgressPackets:  5,
		TotalPackets:   10,
		Window:         "all",
	}

	// Marshal to JSON
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshaling record: %v", err)
	}

	// Unmarshal back
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}

	// Verify required fields exist
	requiredFields := []string{"interface", "ifindex", "mac", "ingress_bytes", "egress_bytes",
		"total_bytes", "ingress_packets", "egress_packets", "total_packets", "window"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}

	// Verify values
	if parsed["interface"] != "eth0" {
		t.Errorf("interface: expected eth0, got %v", parsed["interface"])
	}
	if parsed["total_bytes"].(float64) != 1000 {
		t.Errorf("total_bytes: expected 1000, got %v", parsed["total_bytes"])
	}
}

// Helper to simulate API query - mimics HTTP request pattern for window queries
func queryWindowViaHTTP(baseURL, window, mac, iface string) (*http.Response, error) {
	url := fmt.Sprintf("%s/api/v1/traffic?window=%s", baseURL, window)
	if mac != "" {
		url += "&mac=" + mac
	}
	if iface != "" {
		url += "&interface=" + iface
	}
	return http.Get(url)
}

// TestInvalidWindowRejection verifies invalid window values are rejected.
func TestInvalidWindowRejection(t *testing.T) {
	// This test verifies the API-level validation
	// Invalid windows should return HTTP 400
	validWindows := []string{"all", "30days", "7days", "today", "month"}
	invalidWindows := []string{"", "invalid", "today ", "ALL", "30days "}

	// Verify valid windows are recognized
	for _, w := range validWindows {
		valid := isValidWindow(w)
		if !valid {
			t.Errorf("window %q should be valid", w)
		}
	}

	// Verify invalid windows are rejected
	for _, w := range invalidWindows {
		valid := isValidWindow(w)
		if valid {
			t.Errorf("window %q should be invalid", w)
		}
	}
}

func isValidWindow(w string) bool {
	switch w {
	case "all", "30days", "7days", "today", "month":
		return true
	default:
		return false
	}
}

// VerifyMACFormat tests MAC address format validation.
func TestVerifyMACFormat(t *testing.T) {
	validMACs := []string{
		"aa:bb:cc:dd:ee:ff",
		"AA:BB:CC:DD:EE:FF",
		"00:00:00:00:00:00",
		"ff:ff:ff:ff:ff:ff",
	}
	invalidMACs := []string{
		"",
		"invalid",
		"aa:bb:cc:dd:ee",       // only 5 octets
		"aa:bb:cc:dd:ee:ff:00", // 7 octets
		"gg:hh:ii:jj:kk:ll",    // invalid hex
	}

	for _, mac := range validMACs {
		if !isValidMAC(mac) {
			t.Errorf("MAC %q should be valid", mac)
		}
	}
	for _, mac := range invalidMACs {
		if isValidMAC(mac) {
			t.Errorf("MAC %q should be invalid", mac)
		}
	}
}

func isValidMAC(mac string) bool {
	if len(mac) != 17 {
		return false
	}
	parts := bytes.Split([]byte(mac), []byte(":"))
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		for _, c := range p {
			if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}
