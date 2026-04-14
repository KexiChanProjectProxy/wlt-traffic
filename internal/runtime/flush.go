package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kexi/traffic-count/internal/ebpf"
	"github.com/kexi/traffic-count/internal/storage"
)

type FlushConfig struct {
	Interval time.Duration
}

type FlushLoop struct {
	repo       *storage.Repository
	trafficMap *ebpf.TrafficMap
	cfg        FlushConfig

	mu        sync.RWMutex
	lastFlush time.Time
	flushErr  error
	stopCh    chan struct{}
	stoppedCh chan struct{}
	stopped   bool
}

func NewFlushLoop(repo *storage.Repository, trafficMap *ebpf.TrafficMap, intervalSec int) *FlushLoop {
	return &FlushLoop{
		repo:       repo,
		trafficMap: trafficMap,
		cfg: FlushConfig{
			Interval: time.Duration(intervalSec) * time.Second,
		},
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
}

// Start begins the periodic flush loop.
// It restores checkpoints from SQLite first, then flushes at the configured interval.
func (f *FlushLoop) Start(ctx context.Context) error {
	if err := f.restoreCheckpoints(ctx); err != nil {
		return fmt.Errorf("restoring checkpoints: %w", err)
	}

	go f.runLoop(ctx)
	return nil
}

// Stop gracefully stops the flush loop, performing a final flush before exiting.
func (f *FlushLoop) Stop(ctx context.Context) error {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return nil
	}
	f.stopped = true
	close(f.stopCh)
	f.mu.Unlock()

	// Wait for loop to finish
	select {
	case <-f.stoppedCh:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Final flush to persist any remaining data
	if err := f.Flush(ctx); err != nil {
		return fmt.Errorf("final flush: %w", err)
	}

	return nil
}

// runLoop is the main loop that periodically calls Flush.
func (f *FlushLoop) runLoop(ctx context.Context) {
	defer close(f.stoppedCh)

	ticker := time.NewTicker(f.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-f.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := f.Flush(ctx); err != nil {
				f.mu.Lock()
				f.flushErr = err
				f.mu.Unlock()
				// On flush failure, keep previous checkpoint unchanged and retry next interval
				continue
			}
		}
	}
}

// Flush reads all counters from the eBPF map, computes deltas, and persists to SQLite.
// Flush is idempotent - calling it multiple times with no new traffic will not double-count.
func (f *FlushLoop) Flush(ctx context.Context) error {
	if f.trafficMap == nil || f.trafficMap.Map() == nil {
		return nil // Mock mode - nothing to flush
	}

	iter := f.trafficMap.Iterate()
	currentDateUTC := storage.CurrentDateUTC()

	for {
		ok, key, counters := iter.Next()
		if !ok {
			break
		}

		recordKey := &storage.RecordKey{
			InterfaceName: "", // Not tracked in eBPF key, only ifindex
			Ifindex:       key.Ifindex,
			MAC:           key.Mac,
		}

		// Get checkpoint to compute delta
		cp, err := f.repo.GetCheckpoint(ctx, recordKey)
		if err != nil {
			return fmt.Errorf("getting checkpoint for (%d, %x): %w", key.Ifindex, key.Mac, err)
		}

		var delta storage.TrafficCounters
		if cp != nil && cp.DateUTC == currentDateUTC {
			// Same day - compute delta from checkpoint
			delta = storage.TrafficCounters{
				Bytes:          counters.Bytes - cp.LastRawBytes,
				Packets:        counters.Packets - cp.LastRawPackets,
				IngressBytes:   counters.IngressBytes - cp.LastIngressBytes,
				IngressPackets: counters.IngressPackets - cp.LastIngressPackets,
				EgressBytes:    counters.EgressBytes - cp.LastEgressBytes,
				EgressPackets:  counters.EgressPackets - cp.LastEgressPackets,
			}
		} else {
			// New day or no checkpoint - use raw values as delta
			// This handles both fresh counters and counter wrap on rollover
			delta = storage.TrafficCounters{
				Bytes:          counters.Bytes,
				Packets:        counters.Packets,
				IngressBytes:   counters.IngressBytes,
				IngressPackets: counters.IngressPackets,
				EgressBytes:    counters.EgressBytes,
				EgressPackets:  counters.EgressPackets,
			}
		}

		// Skip if no new traffic (delta would be zero or negative due to counter wrap)
		if delta.Bytes == 0 && delta.Packets == 0 &&
			delta.IngressBytes == 0 && delta.IngressPackets == 0 &&
			delta.EgressBytes == 0 && delta.EgressPackets == 0 {
			// Still update checkpoint with current raw values
			if err := f.repo.UpsertCheckpoint(ctx, recordKey, currentDateUTC, &storage.TrafficCounters{
				Bytes:          counters.Bytes,
				Packets:        counters.Packets,
				IngressBytes:   counters.IngressBytes,
				IngressPackets: counters.IngressPackets,
				EgressBytes:    counters.EgressBytes,
				EgressPackets:  counters.EgressPackets,
			}); err != nil {
				return fmt.Errorf("updating checkpoint: %w", err)
			}
			continue
		}

		// Persist all three in a single transaction
		if err := f.persistAll(ctx, recordKey, currentDateUTC, &delta, counters); err != nil {
			return fmt.Errorf("persisting for (%d, %x): %w", key.Ifindex, key.Mac, err)
		}
	}

	f.mu.Lock()
	f.lastFlush = time.Now()
	f.flushErr = nil
	f.mu.Unlock()

	return nil
}

// persistAll writes checkpoint, totals, and daily bucket in a single transaction.
func (f *FlushLoop) persistAll(ctx context.Context, key *storage.RecordKey, dateUTC string, delta *storage.TrafficCounters, raw *ebpf.TrafficCounter) error {
	tx, err := f.repo.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Upsert checkpoint with current raw values
	cpQuery := `
		INSERT INTO collector_checkpoints (interface_name, ifindex, mac, date_utc, last_raw_bytes, last_raw_packets, last_ingress_bytes, last_ingress_packets, last_egress_bytes, last_egress_packets)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(interface_name, ifindex, mac) DO UPDATE SET
			date_utc               = excluded.date_utc,
			last_raw_bytes         = excluded.last_raw_bytes,
			last_raw_packets       = excluded.last_raw_packets,
			last_ingress_bytes     = excluded.last_ingress_bytes,
			last_ingress_packets   = excluded.last_ingress_packets,
			last_egress_bytes      = excluded.last_egress_bytes,
			last_egress_packets    = excluded.last_egress_packets,
			updated_at             = datetime('now')
	`
	_, err = tx.ExecContext(ctx, cpQuery,
		key.InterfaceName,
		key.Ifindex,
		key.MAC[:],
		dateUTC,
		raw.Bytes,
		raw.Packets,
		raw.IngressBytes,
		raw.IngressPackets,
		raw.EgressBytes,
		raw.EgressPackets,
	)
	if err != nil {
		return fmt.Errorf("upserting checkpoint: %w", err)
	}

	// 2. Upsert all-time totals
	totalQuery := `
		INSERT INTO mac_traffic_totals (interface_name, ifindex, mac, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(interface_name, ifindex, mac) DO UPDATE SET
			bytes           = bytes           + excluded.bytes,
			packets         = packets         + excluded.packets,
			ingress_bytes   = ingress_bytes   + excluded.ingress_bytes,
			ingress_packets = ingress_packets + excluded.ingress_packets,
			egress_bytes    = egress_bytes    + excluded.egress_bytes,
			egress_packets  = egress_packets  + excluded.egress_packets,
			updated_at      = datetime('now')
	`
	_, err = tx.ExecContext(ctx, totalQuery,
		key.InterfaceName,
		key.Ifindex,
		key.MAC[:],
		delta.Bytes,
		delta.Packets,
		delta.IngressBytes,
		delta.IngressPackets,
		delta.EgressBytes,
		delta.EgressPackets,
	)
	if err != nil {
		return fmt.Errorf("upserting total: %w", err)
	}

	// 3. Upsert daily bucket
	dailyQuery := `
		INSERT INTO mac_traffic_daily (interface_name, ifindex, mac, date_utc, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(interface_name, ifindex, mac, date_utc) DO UPDATE SET
			bytes           = bytes           + excluded.bytes,
			packets         = packets         + excluded.packets,
			ingress_bytes   = ingress_bytes   + excluded.ingress_bytes,
			ingress_packets = ingress_packets + excluded.ingress_packets,
			egress_bytes    = egress_bytes    + excluded.egress_bytes,
			egress_packets  = egress_packets  + excluded.egress_packets,
			updated_at      = datetime('now')
	`
	_, err = tx.ExecContext(ctx, dailyQuery,
		key.InterfaceName,
		key.Ifindex,
		key.MAC[:],
		dateUTC,
		delta.Bytes,
		delta.Packets,
		delta.IngressBytes,
		delta.IngressPackets,
		delta.EgressBytes,
		delta.EgressPackets,
	)
	if err != nil {
		return fmt.Errorf("upserting daily: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// restoreCheckpoints loads checkpoints from SQLite into memory for delta computation.
// This is called at startup to resume from file-backed state.
func (f *FlushLoop) restoreCheckpoints(ctx context.Context) error {
	// Checkpoints are stored in SQLite and read on-demand during Flush.
	// This method is called at startup to verify the database is accessible
	// and to prepare for recovery.
	if f.repo == nil {
		return nil
	}

	// Verify we can list checkpoints (table exists and is accessible)
	_, err := f.repo.ListCheckpoints(ctx, "")
	if err != nil {
		return fmt.Errorf("listing checkpoints: %w", err)
	}

	return nil
}

// LastFlush returns the time of the last successful flush.
func (f *FlushLoop) LastFlush() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.lastFlush
}

// FlushError returns the last flush error, if any.
func (f *FlushLoop) FlushError() error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.flushErr
}

// IsStale returns true if the last flush was older than 2x the flush interval.
func (f *FlushLoop) IsStale() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.lastFlush.IsZero() {
		return true
	}

	age := time.Since(f.lastFlush)
	return age > f.cfg.Interval*2
}
