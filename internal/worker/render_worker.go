// Package worker runs the background render farm loop and the housekeeping
// reaper. Both respect context cancellation and stop gracefully.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/service/render"
)

// Output describes the artefact produced by an encoder.
type Output struct {
	URI   string
	Bytes int64
}

// Renderer performs the actual encode. Production deployments wrap the encoder
// binary; tests provide a deterministic stub.
type Renderer interface {
	Render(ctx context.Context, job *domain.RenderJob) (Output, error)
}

// RendererFunc adapts a function to the Renderer interface.
type RendererFunc func(ctx context.Context, job *domain.RenderJob) (Output, error)

// Render calls the wrapped function.
func (f RendererFunc) Render(ctx context.Context, job *domain.RenderJob) (Output, error) {
	return f(ctx, job)
}

// SyntheticRenderer produces a deterministic artefact reference. It is the
// default local encoder so the platform runs end to end without a farm.
type SyntheticRenderer struct {
	BytesPerSecond int64
	Delay          time.Duration
}

// Render simulates an encode by deriving the artefact size from the preset.
func (r SyntheticRenderer) Render(ctx context.Context, job *domain.RenderJob) (Output, error) {
	if r.Delay > 0 {
		select {
		case <-ctx.Done():
			return Output{}, ctx.Err()
		case <-time.After(r.Delay):
		}
	}
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	perSecond := r.BytesPerSecond
	if perSecond <= 0 {
		perSecond = 2 << 20
	}
	multiplier := int64(1)
	switch job.Preset {
	case "proxy_540p":
		multiplier = 1
	case "web_1080p":
		multiplier = 4
	case "master_2160p":
		multiplier = 12
	}
	return Output{
		URI:   fmt.Sprintf("cutvideo://renders/%s/%s.mov", job.ProjectID, job.ID),
		Bytes: perSecond * multiplier,
	}, nil
}

// Pool polls the render queue and executes claimed jobs.
type Pool struct {
	service  *render.Service
	renderer Renderer
	cfg      config.WorkerConfig
	pool     string
	logger   *slog.Logger

	wg      sync.WaitGroup
	mu      sync.Mutex
	running bool
}

// NewPool builds a render worker pool.
func NewPool(service *render.Service, renderer Renderer, cfg config.WorkerConfig, pool string, logger *slog.Logger) *Pool {
	if logger == nil {
		logger = logging.Discard()
	}
	if renderer == nil {
		renderer = SyntheticRenderer{}
	}
	if pool == "" {
		pool = render.DefaultPool
	}
	return &Pool{service: service, renderer: renderer, cfg: cfg, pool: pool, logger: logger}
}

// Start launches the configured number of worker goroutines.
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return
	}
	p.running = true
	concurrency := p.cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	for index := 0; index < concurrency; index++ {
		p.wg.Add(1)
		go p.loop(ctx, index)
	}
	p.logger.Info("render worker pool started", "concurrency", concurrency, "pool", p.pool)
}

// Stop waits for every worker goroutine to return.
func (p *Pool) Stop() {
	p.wg.Wait()
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
	p.logger.Info("render worker pool stopped")
}

func (p *Pool) loop(ctx context.Context, index int) {
	defer p.wg.Done()
	logger := p.logger.With("worker", index)
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Debug("worker stopping", "reason", ctx.Err())
			return
		case <-ticker.C:
			processed, err := p.ProcessOnce(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				logger.Warn("render cycle failed", "error", err.Error())
				continue
			}
			if processed {
				logger.Debug("render cycle completed")
			}
		}
	}
}

// holdLease refreshes the lease of the job being encoded until the caller closes
// done. It returns as soon as the encode finishes, the process is shutting down
// or the job no longer holds a lease it may extend.
func (p *Pool) holdLease(ctx context.Context, jobID string, done <-chan struct{}) {
	interval := p.cfg.LeaseRenewInterval
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.service.RenewLease(ctx, jobID); err != nil {
				p.logger.Warn("render lease renewal stopped", "job_id", jobID, "error", err.Error())
				return
			}
		}
	}
}

// ProcessOnce claims at most one job and drives it to a terminal state. It
// reports whether a job was processed. Tests call it directly so the queue can be
// exercised without timers.
func (p *Pool) ProcessOnce(ctx context.Context) (bool, error) {
	job, err := p.service.Claim(ctx, p.pool)
	if err != nil {
		if apierr.IsCode(err, apierr.CodeQuotaExhausted) {
			return false, nil
		}
		return false, err
	}
	if job == nil {
		return false, nil
	}
	if _, err := p.service.Start(ctx, job.ID); err != nil {
		return false, err
	}
	// Hold the lease while the encoder works so the housekeeping reaper does not
	// reclaim a seat that is still in use.
	heartbeatDone := make(chan struct{})
	var heartbeat sync.WaitGroup
	heartbeat.Add(1)
	go func() {
		defer heartbeat.Done()
		p.holdLease(ctx, job.ID, heartbeatDone)
	}()
	output, renderErr := p.renderer.Render(ctx, job)
	close(heartbeatDone)
	heartbeat.Wait()
	if renderErr != nil {
		if errors.Is(renderErr, context.Canceled) || errors.Is(renderErr, context.DeadlineExceeded) {
			// The process is shutting down. Release the seat and return the job to
			// the queue with a detached context so cleanup still reaches the store.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := p.service.Abandon(cleanupCtx, job.ID, "worker shutdown"); err != nil {
				return false, err
			}
			return false, renderErr
		}
		if _, err := p.service.FailAttempt(ctx, job.ID, renderErr.Error()); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := p.service.Complete(ctx, render.CompleteInput{
		JobID:       job.ID,
		OutputURI:   output.URI,
		OutputBytes: output.Bytes,
	}); err != nil {
		return false, err
	}
	return true, nil
}
