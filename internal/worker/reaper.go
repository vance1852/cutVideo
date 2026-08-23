package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vance1852/cutVideo/internal/idempotency"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/service/auth"
	"github.com/vance1852/cutVideo/internal/service/render"
)

// ReaperReport summarizes one housekeeping pass.
type ReaperReport struct {
	RequeuedJobs    int
	PurgedSessions  int
	PurgedIdemKeys  int
	CompletedAt     time.Time
	ObservedFailure error
}

// Reaper recovers abandoned render leases and removes expired bookkeeping rows.
type Reaper struct {
	renders  *render.Service
	auth     *auth.Service
	guard    *idempotency.Guard
	period   time.Duration
	batch    int
	logger   *slog.Logger
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewReaper builds the housekeeping worker.
func NewReaper(renders *render.Service, authService *auth.Service, guard *idempotency.Guard, period time.Duration, logger *slog.Logger) *Reaper {
	if logger == nil {
		logger = logging.Discard()
	}
	if period <= 0 {
		period = 30 * time.Second
	}
	return &Reaper{renders: renders, auth: authService, guard: guard, period: period, batch: 25, logger: logger}
}

// Start runs the reaper until the context is canceled.
func (r *Reaper) Start(ctx context.Context) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.period)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				r.logger.Debug("reaper stopping", "reason", ctx.Err())
				return
			case <-ticker.C:
				report := r.RunOnce(ctx)
				if report.ObservedFailure != nil {
					if errors.Is(report.ObservedFailure, context.Canceled) {
						return
					}
					r.logger.Warn("housekeeping pass failed", "error", report.ObservedFailure.Error())
					continue
				}
				if report.RequeuedJobs > 0 || report.PurgedSessions > 0 || report.PurgedIdemKeys > 0 {
					r.logger.Info("housekeeping pass completed",
						"requeued_jobs", report.RequeuedJobs,
						"purged_sessions", report.PurgedSessions,
						"purged_idempotency_keys", report.PurgedIdemKeys,
					)
				}
			}
		}
	}()
}

// Stop waits for the reaper goroutine to return.
func (r *Reaper) Stop() {
	r.stopOnce.Do(func() { r.wg.Wait() })
}

// RunOnce performs a single housekeeping pass.
func (r *Reaper) RunOnce(ctx context.Context) ReaperReport {
	report := ReaperReport{}
	requeued, err := r.renders.RequeueExpiredLeases(ctx, r.batch)
	if err != nil {
		report.ObservedFailure = err
		return report
	}
	report.RequeuedJobs = requeued

	if r.auth != nil {
		purged, err := r.auth.PurgeExpiredSessions(ctx)
		if err != nil {
			report.ObservedFailure = err
			return report
		}
		report.PurgedSessions = purged
	}
	if r.guard != nil {
		purged, err := r.guard.Purge(ctx)
		if err != nil {
			report.ObservedFailure = err
			return report
		}
		report.PurgedIdemKeys = purged
	}
	report.CompletedAt = time.Now()
	return report
}
