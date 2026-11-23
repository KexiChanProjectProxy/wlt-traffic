package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kexi/traffic-count/internal/ebpf"
	"github.com/kexi/traffic-count/internal/storage"
)

type fakeSource struct {
	samples map[ebpf.Key]ebpf.Value
	deleted []ebpf.Key
}

func (f *fakeSource) Snapshot() ([]ebpf.Sample, error) {
	var out []ebpf.Sample
	for k, v := range f.samples {
		out = append(out, ebpf.Sample{Key: k, Value: v})
	}
	return out, nil
}
func (f *fakeSource) Delete(keys []ebpf.Key) error {
	for _, k := range keys {
		delete(f.samples, k)
	}
	f.deleted = append(f.deleted, keys...)
	return nil
}
func (f *fakeSource) NameOf(i uint32) string { return map[uint32]string{2: "br-lan", 3: "br-guest"}[i] }

type fakeStore struct {
	fail  bool
	added map[string]storage.Counters // day|iface|mac
}

func (f *fakeStore) AddDeltas(_ context.Context, ds []storage.Delta, _ time.Time) error {
	if f.fail {
		return errors.New("disk full")
	}
	for _, d := range ds {
		k := d.Day + "|" + d.Iface + "|" + d.MAC
		f.added[k] = f.added[k].Add(d.Counters)
	}
	return nil
}

var (
	devA = ebpf.Key{Ifindex: 2, MAC: [6]byte{0xaa, 0, 0, 0, 0, 1}}
	devB = ebpf.Key{Ifindex: 3, MAC: [6]byte{0xaa, 0, 0, 0, 0, 2}}
)

func TestAccountantDeltasAcrossDaysAndFailures(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	src := &fakeSource{samples: map[ebpf.Key]ebpf.Value{}}
	st := &fakeStore{added: map[string]storage.Counters{}}
	a := NewAccountant(src, st, loc)
	ctx := context.Background()
	// 2026-11-23 23:59:50 CST
	t0 := time.Date(2026, 11, 23, 23, 59, 50, 0, loc)

	src.samples[devA] = ebpf.Value{UpBytes: 100, UpPackets: 1, DownBytes: 1000, DownPackets: 2}
	if err := a.Tick(ctx, t0); err != nil {
		t.Fatal(err)
	}
	// Same counters again: nothing new.
	if err := a.Tick(ctx, t0.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Crosses local midnight: only the increase goes to the new day.
	src.samples[devA] = ebpf.Value{UpBytes: 150, UpPackets: 2, DownBytes: 3000, DownPackets: 3}
	src.samples[devB] = ebpf.Value{UpBytes: 7, UpPackets: 1}
	if err := a.Tick(ctx, t0.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	want := map[string]storage.Counters{
		"2026-11-23|br-lan|aa:00:00:00:00:01":   {100, 1, 1000, 2},
		"2026-11-24|br-lan|aa:00:00:00:00:01":   {50, 1, 2000, 1},
		"2026-11-24|br-guest|aa:00:00:00:00:02": {7, 1, 0, 0},
	}
	for k, v := range want {
		if st.added[k] != v {
			t.Errorf("%s = %+v, want %+v", k, st.added[k], v)
		}
	}
	if len(st.added) != len(want) {
		t.Errorf("unexpected rows: %+v", st.added)
	}
	live := a.Live()
	if len(live) != 2 || live[0].MAC != "aa:00:00:00:00:01" || live[0].DownloadBps != 2000/15 {
		t.Errorf("live = %+v", live)
	}

	// Store failure: counted traffic is kept and written on the next tick.
	st.fail = true
	src.samples[devA] = ebpf.Value{UpBytes: 250, UpPackets: 3, DownBytes: 3000, DownPackets: 3}
	if err := a.Tick(ctx, t0.Add(30*time.Second)); err == nil {
		t.Fatal("expected error")
	}
	st.fail = false
	if err := a.Tick(ctx, t0.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := st.added["2026-11-24|br-lan|aa:00:00:00:00:01"]; got.UpBytes != 150 {
		t.Errorf("retry lost data: %+v", got)
	}
}

func TestAccountantRecreatedEntryAndIdleEviction(t *testing.T) {
	src := &fakeSource{samples: map[ebpf.Key]ebpf.Value{devA: {UpBytes: 500}}}
	st := &fakeStore{added: map[string]storage.Counters{}}
	a := NewAccountant(src, st, time.UTC)
	a.idleEvict = time.Minute
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a.Tick(ctx, t0)
	// Entry evicted by the kernel LRU and recreated with a smaller value.
	src.samples[devA] = ebpf.Value{UpBytes: 30}
	a.Tick(ctx, t0.Add(10*time.Second))
	if got := st.added["2026-01-01|br-lan|aa:00:00:00:00:01"].UpBytes; got != 530 {
		t.Fatalf("got %d, want 530", got)
	}
	// Idle for longer than idleEvict: removed from the kernel map.
	a.Tick(ctx, t0.Add(2*time.Minute))
	if len(src.deleted) != 1 {
		t.Fatalf("idle entry not evicted: %+v", src.deleted)
	}
}
