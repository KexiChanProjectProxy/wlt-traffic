package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kexi/traffic-count/internal/storage"
)

// HousekeepingConfig holds schedule parameters for housekeeping tasks.
type HousekeepingConfig struct {
	Interval time.Duration
}

// HousekeepingLoop runs scheduled maintenance: UTC rollover detection and retention pruning.
type HousekeepingLoop struct {
	repo          *storage.Repository
	cfg           HousekeepingConfig
	mu            sync.RWMutex
	lastRun       time.Time
	lastPruneDate string
	pruneErr      error
	stopCh        chan struct{}
	stoppedCh     chan struct{}
	stopped       bool
}

// NewHousekeepingLoop creates a HousekeepingLoop with the given repository and interval.
func NewHousekeepingLoop(repo *storage.Repository, intervalSec int) *HousekeepingLoop {
	return &HousekeepingLoop{
		repo: repo,
		cfg: HousekeepingConfig{
			Interval: time.Duration(intervalSec) * time.Second,
		},
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
}

// Start begins the periodic housekeeping loop.
func (h *HousekeepingLoop) Start(ctx context.Context) error {
	// Run once at startup to catch any missed pruning and detect rollover.
	if err := h.runOnce(ctx); err != nil {
		return fmt.Errorf("initial housekeeping run: %w", err)
	}

	go h.runLoop(ctx)
	return nil
}

// Stop gracefully stops the housekeeping loop.
func (h *HousekeepingLoop) Stop(ctx context.Context) error {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return nil
	}
	h.stopped = true
	close(h.stopCh)
	h.mu.Unlock()

	select {
	case <-h.stoppedCh:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// runLoop runs the housekeeping tasks on the configured interval.
func (h *HousekeepingLoop) runLoop(ctx context.Context) {
	defer close(h.stoppedCh)

	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.runOnce(ctx); err != nil {
				h.mu.Lock()
				h.pruneErr = err
				h.mu.Unlock()
				// Continue running even on error; log and retry next interval.
				continue
			}
		}
	}
}

// runOnce executes a single housekeeping run: prune old daily rows.
func (h *HousekeepingLoop) runOnce(ctx context.Context) error {
	if h.repo == nil {
		return nil
	}

	cutoffDateUTC := storage.PruneCutoffDate()

	h.mu.Lock()
	h.lastRun = time.Now()
	h.mu.Unlock()

	rowsDeleted, err := h.repo.PruneDaily(ctx, cutoffDateUTC)
	if err != nil {
		h.mu.Lock()
		h.pruneErr = err
		h.mu.Unlock()
		return fmt.Errorf("pruning daily records (cutoff %s): %w", cutoffDateUTC, err)
	}

	h.mu.Lock()
	h.lastPruneDate = cutoffDateUTC
	h.pruneErr = nil
	h.mu.Unlock()

	if rowsDeleted > 0 {
		fmt.Printf("housekeeping: pruned %d daily rows older than %s\n", rowsDeleted, cutoffDateUTC)
	}

	return nil
}

// LastRun returns the time of the last housekeeping run.
func (h *HousekeepingLoop) LastRun() time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastRun
}

// LastPruneCutoff returns the cutoff date used in the last prune operation.
func (h *HousekeepingLoop) LastPruneCutoff() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastPruneDate
}

// PruneError returns the last prune error, if any.
func (h *HousekeepingLoop) PruneError() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pruneErr
}
