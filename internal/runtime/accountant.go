// Package runtime turns raw kernel counters into persisted per-device traffic.
package runtime

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/kexi/traffic-count/internal/ebpf"
	"github.com/kexi/traffic-count/internal/storage"
)

// Source is the kernel side (implemented by *ebpf.Collector).
type Source interface {
	Snapshot() ([]ebpf.Sample, error)
	Delete(keys []ebpf.Key) error
	NameOf(ifindex uint32) string
}

// Store is the persistence side (implemented by *storage.Repository).
type Store interface {
	AddDeltas(ctx context.Context, deltas []storage.Delta, now time.Time) error
}

// LiveRate is a device's throughput over the last sampling interval.
type LiveRate struct {
	MAC          string `json:"mac"`
	UploadBps    uint64 `json:"upload_bps"`   // bytes per second
	DownloadBps  uint64 `json:"download_bps"` // bytes per second
	LastActivity int64  `json:"last_activity"`
}

type baseline struct {
	value      ebpf.Value
	lastChange time.Time
}

type pendingKey struct{ day, iface, mac string }

// Accountant samples the kernel counters and persists the increase since the
// previous sample.
//
// The kernel map lives exactly as long as this process (it is not pinned), so
// the baseline is kept in memory: a fresh process starts from an empty map and
// an empty baseline, and nothing is counted twice across restarts, day
// boundaries or interface re-creation. Increases that could not be written are
// kept in memory and retried on the next tick.
type Accountant struct {
	src       Source
	store     Store
	loc       *time.Location
	idleEvict time.Duration
	logf      func(string, ...any)

	mu         sync.Mutex
	base       map[ebpf.Key]*baseline
	pending    map[pendingKey]storage.Counters
	live       map[string]*LiveRate
	lastSample time.Time
	lastFlush  time.Time
	flushErr   error
}

func NewAccountant(src Source, store Store, loc *time.Location) *Accountant {
	if loc == nil {
		loc = time.Local
	}
	return &Accountant{
		src:       src,
		store:     store,
		loc:       loc,
		idleEvict: time.Hour,
		logf:      log.Printf,
		base:      map[ebpf.Key]*baseline{},
		pending:   map[pendingKey]storage.Counters{},
		live:      map[string]*LiveRate{},
	}
}

func sub(a, b uint64) uint64 {
	if a < b {
		// Cannot happen for a live entry; if the entry was recreated (e.g. LRU
		// eviction), everything in it is new.
		return a
	}
	return a - b
}

// Tick samples the counters once and persists the deltas.
func (a *Accountant) Tick(ctx context.Context, now time.Time) error {
	samples, snapErr := a.src.Snapshot()

	a.mu.Lock()
	day := now.In(a.loc).Format(time.DateOnly)
	elapsed := now.Sub(a.lastSample).Seconds()
	if a.lastSample.IsZero() || elapsed <= 0 {
		elapsed = 0
	}
	liveUp := map[string]uint64{}
	liveDown := map[string]uint64{}
	seen := make(map[ebpf.Key]bool, len(samples))
	var evict []ebpf.Key

	for _, s := range samples {
		seen[s.Key] = true
		b := a.base[s.Key]
		if b == nil {
			b = &baseline{lastChange: now}
			a.base[s.Key] = b
		}
		d := storage.Counters{
			UpBytes:     sub(s.Value.UpBytes, b.value.UpBytes),
			UpPackets:   sub(s.Value.UpPackets, b.value.UpPackets),
			DownBytes:   sub(s.Value.DownBytes, b.value.DownBytes),
			DownPackets: sub(s.Value.DownPackets, b.value.DownPackets),
		}
		b.value = s.Value
		mac := ebpf.MACString(s.Key.MAC)
		if d.IsZero() {
			if now.Sub(b.lastChange) > a.idleEvict {
				evict = append(evict, s.Key)
			}
			continue
		}
		b.lastChange = now
		pk := pendingKey{day: day, iface: a.src.NameOf(s.Key.Ifindex), mac: mac}
		a.pending[pk] = a.pending[pk].Add(d)
		liveUp[mac] += d.UpBytes
		liveDown[mac] += d.DownBytes
	}
	// Entries that disappeared from the map (deleted or LRU-evicted).
	if snapErr == nil {
		for k := range a.base {
			if !seen[k] {
				delete(a.base, k)
			}
		}
	}
	for _, k := range evict {
		delete(a.base, k)
	}

	// Live rates.
	if elapsed > 0 {
		for mac, r := range a.live {
			r.UploadBps, r.DownloadBps = 0, 0
			if now.Unix()-r.LastActivity > int64(a.idleEvict.Seconds()) {
				delete(a.live, mac)
			}
		}
		for mac := range liveUp {
			r := a.live[mac]
			if r == nil {
				r = &LiveRate{MAC: mac}
				a.live[mac] = r
			}
			r.UploadBps = uint64(float64(liveUp[mac]) / elapsed)
			r.DownloadBps = uint64(float64(liveDown[mac]) / elapsed)
			r.LastActivity = now.Unix()
		}
	}
	a.lastSample = now

	deltas := make([]storage.Delta, 0, len(a.pending))
	for k, c := range a.pending {
		deltas = append(deltas, storage.Delta{Day: k.day, Iface: k.iface, MAC: k.mac, Counters: c})
	}
	a.mu.Unlock()

	if len(evict) > 0 {
		if err := a.src.Delete(evict); err != nil {
			a.logf("evict idle counters: %v", err)
		}
	}

	err := a.store.AddDeltas(ctx, deltas, now)

	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		for _, d := range deltas {
			pk := pendingKey{d.Day, d.Iface, d.MAC}
			// Only subtract what was written; Tick is not reentrant but be exact.
			left := a.pending[pk]
			left.UpBytes -= d.Counters.UpBytes
			left.UpPackets -= d.Counters.UpPackets
			left.DownBytes -= d.Counters.DownBytes
			left.DownPackets -= d.Counters.DownPackets
			if left.IsZero() {
				delete(a.pending, pk)
			} else {
				a.pending[pk] = left
			}
		}
	}
	switch {
	case err != nil:
		a.flushErr = fmt.Errorf("persist traffic: %w", err)
	case snapErr != nil:
		a.flushErr = snapErr
	default:
		a.flushErr = nil
		a.lastFlush = now
	}
	return a.flushErr
}

// Run ticks every interval until ctx is done, then ticks once more so nothing
// counted is lost on shutdown.
func (a *Accountant) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	a.Tick(ctx, time.Now()) // establish the baseline / first rates
	for {
		select {
		case <-ctx.Done():
			if err := a.Tick(context.Background(), time.Now()); err != nil {
				a.logf("final flush: %v", err)
			}
			return
		case now := <-t.C:
			if err := a.Tick(ctx, now); err != nil {
				a.logf("flush: %v", err)
			}
		}
	}
}

// LastFlush and FlushError report the most recent tick.
func (a *Accountant) LastFlush() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastFlush
}

func (a *Accountant) FlushError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.flushErr
}

// Live returns current per-device rates, busiest first.
func (a *Accountant) Live() []LiveRate {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]LiveRate, 0, len(a.live))
	for _, r := range a.live {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].UploadBps+out[i].DownloadBps, out[j].UploadBps+out[j].DownloadBps
		if ti != tj {
			return ti > tj
		}
		return out[i].MAC < out[j].MAC
	})
	return out
}

// Pending reports bytes counted but not yet persisted (for status).
func (a *Accountant) Pending() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// Housekeep prunes daily rows older than retentionDays, once now and then daily.
func Housekeep(ctx context.Context, repo *storage.Repository, loc *time.Location, retentionDays int, every time.Duration, logf func(string, ...any)) {
	run := func() {
		cutoff := time.Now().In(loc).AddDate(0, 0, -retentionDays).Format(time.DateOnly)
		n, err := repo.PruneDaily(ctx, cutoff)
		if err != nil {
			logf("housekeeping: %v", err)
		} else if n > 0 {
			logf("housekeeping: pruned %d daily rows before %s", n, cutoff)
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
