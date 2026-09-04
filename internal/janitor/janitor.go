// Package janitor runs the periodic housekeeping the contract implies: keys
// that were presigned but never attached are collected after 24 hours, and
// refresh tokens are dropped once they can no longer be exchanged.
package janitor

import (
	"context"
	"log/slog"
	"time"

	"domieface/com/internal/store"
	"domieface/com/internal/uploads"
)

// batchSize caps how many abandoned keys one sweep reclaims, so a large backlog
// is worked through over several ticks instead of one long transaction.
const batchSize = 500

// Janitor sweeps abandoned uploads and expired refresh tokens.
type Janitor struct {
	store     store.Store
	presigner *uploads.Presigner
	gcAfter   time.Duration
	interval  time.Duration
}

// New builds a janitor. gcAfter is the grace period an unattached upload gets.
func New(st store.Store, presigner *uploads.Presigner, gcAfter, interval time.Duration) *Janitor {
	return &Janitor{store: st, presigner: presigner, gcAfter: gcAfter, interval: interval}
}

// Run sweeps on a ticker until ctx is cancelled. It sweeps once on start so a
// short-lived process still does its share of the work.
func (j *Janitor) Run(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.sweep(ctx)
	for {
		select {
		case <-ticker.C:
			j.sweep(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (j *Janitor) sweep(ctx context.Context) {
	// Bound each sweep so a slow database cannot hold the ticker up
	// indefinitely, and so shutdown is not blocked waiting on it.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	j.collectAbandonedUploads(ctx)
	j.deleteExpiredTokens(ctx)
}

func (j *Janitor) collectAbandonedUploads(ctx context.Context) {
	cutoff := time.Now().Add(-j.gcAfter)

	keys, err := j.store.Uploads().ReleaseAbandoned(ctx, cutoff, batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "releasing abandoned uploads failed", "error", err)
		return
	}
	if len(keys) == 0 {
		return
	}

	// The rows are already gone. If the object delete fails we log and move on
	// rather than retrying forever: the storage bucket's own lifecycle rule is
	// the backstop, and an orphaned object costs pennies where a stuck janitor
	// would block every later sweep.
	if err := j.presigner.Delete(ctx, keys); err != nil {
		slog.ErrorContext(ctx, "deleting abandoned objects failed",
			"error", err, "keys", len(keys))
		return
	}
	slog.InfoContext(ctx, "abandoned uploads collected", "keys", len(keys))
}

func (j *Janitor) deleteExpiredTokens(ctx context.Context) {
	// Keep tokens a week past expiry: replaying one that leaked shortly before
	// it expired should still trip the family revocation rather than silently
	// look like an unknown token.
	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	deleted, err := j.store.Tokens().DeleteExpired(ctx, cutoff)
	if err != nil {
		slog.ErrorContext(ctx, "deleting expired refresh tokens failed", "error", err)
		return
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "expired refresh tokens deleted", "count", deleted)
	}
}
