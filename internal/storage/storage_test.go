package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestAddDeltasAccumulatesAndQueries(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	a, b := "aa:aa:aa:aa:aa:01", "aa:aa:aa:aa:aa:02"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(r.AddDeltas(ctx, []Delta{
		{"2026-11-22", "br-lan", a, Counters{100, 1, 1000, 2}},
		{"2026-11-23", "br-lan", a, Counters{10, 1, 20, 1}},
		{"2026-11-23", "br-lan", b, Counters{5000, 5, 1, 1}},
		{"2026-11-23", "br-guest", a, Counters{1, 1, 1, 1}},
	}, now))
	must(r.AddDeltas(ctx, []Delta{{"2026-11-23", "br-lan", a, Counters{10, 1, 20, 1}}}, now))

	rows, n, err := r.QueryTraffic(ctx, Query{From: "2026-11-23", To: "2026-11-23", Limit: 10})
	must(err)
	if n != 3 || len(rows) != 3 {
		t.Fatalf("got %d rows (count %d), want 3", len(rows), n)
	}
	if rows[0].MAC != b {
		t.Fatalf("not sorted by total desc: %+v", rows)
	}
	if got := (Counters{20, 2, 40, 2}); rows[1].Counters != got || rows[1].Iface != "br-lan" {
		t.Fatalf("accumulation wrong: %+v", rows[1])
	}

	rows, _, err = r.QueryTraffic(ctx, Query{From: "2026-11-23", To: "2026-11-23", GroupByMAC: true, MAC: "AA:AA:AA:AA:AA:01"})
	must(err)
	if len(rows) != 1 || rows[0].Counters != (Counters{21, 3, 41, 3}) || len(rows[0].Ifaces) != 2 {
		t.Fatalf("group by mac wrong: %+v", rows)
	}

	rows, _, err = r.QueryTraffic(ctx, Query{GroupByMAC: true, MAC: a}) // all time
	must(err)
	if rows[0].Counters != (Counters{121, 4, 1041, 5}) {
		t.Fatalf("all-time wrong: %+v", rows[0])
	}

	rows, n, err = r.QueryTraffic(ctx, Query{From: "2026-11-23", To: "2026-11-23", Limit: 1, Offset: 5})
	must(err)
	if len(rows) != 0 || n != 3 {
		t.Fatalf("offset past end: rows=%d count=%d", len(rows), n)
	}
}

func TestSeriesFillsGapsAndPrune(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	mac := "aa:aa:aa:aa:aa:01"
	if err := r.AddDeltas(ctx, []Delta{
		{"2026-11-20", "br-lan", mac, Counters{1, 1, 2, 1}},
		{"2026-11-22", "br-lan", mac, Counters{3, 1, 4, 1}},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	pts, err := r.Series(ctx, mac, "", "2026-11-19", "2026-11-22")
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 4 || pts[0].Counters.UpBytes != 0 || pts[1].Counters.DownBytes != 2 || pts[3].Counters.UpBytes != 3 {
		t.Fatalf("series wrong: %+v", pts)
	}
	n, err := r.PruneDaily(ctx, "2026-11-21")
	if err != nil || n != 1 {
		t.Fatalf("prune: n=%d err=%v", n, err)
	}
	// Totals survive pruning.
	rows, _, _ := r.QueryTraffic(ctx, Query{MAC: mac, GroupByMAC: true})
	if rows[0].Counters.UpBytes != 4 {
		t.Fatalf("totals pruned: %+v", rows)
	}
}

func TestHugeCountersDoNotFail(t *testing.T) {
	r := newRepo(t)
	if err := r.AddDeltas(context.Background(), []Delta{{"2026-11-23", "x", "aa:aa:aa:aa:aa:01", Counters{UpBytes: 1 << 63}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
}
