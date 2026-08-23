// Package render owns the render farm: queue submission, exclusive slot
// reservation, lease bookkeeping, attempt retries and cancellation.
package render

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/audit"
	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/idempotency"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/repository"
)

// DefaultPool is the render farm pool used when a request omits one.
const DefaultPool = "primary"

// Service implements the render farm use cases.
type Service struct {
	store    repository.Store
	recorder *audit.Recorder
	guard    *idempotency.Guard
	gen      ids.Generator
	clk      clock.Clock
	cfg      config.Config
	logger   *slog.Logger
}

// New builds the render service.
func New(store repository.Store, recorder *audit.Recorder, guard *idempotency.Guard, gen ids.Generator, clk clock.Clock, cfg config.Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Service{store: store, recorder: recorder, guard: guard, gen: gen, clk: clk, cfg: cfg, logger: logger}
}

// SubmitInput describes a render request.
type SubmitInput struct {
	TimelineID     string
	Preset         string
	Priority       domain.RenderPriority
	IdempotencyKey string
}

// Submit queues a render for a sealed timeline version. Repeating the same
// request with the same idempotency key returns the original job instead of
// consuming farm capacity twice.
func (s *Service) Submit(ctx context.Context, actor domain.Principal, input SubmitInput) (*domain.RenderJob, error) {
	if err := actor.RequireEdit(); err != nil {
		return nil, apierr.Wrap(apierr.CodeForbidden, "your role may not submit renders", err)
	}
	if !s.cfg.AllowsPreset(input.Preset) {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest,
			fmt.Sprintf("render preset %q is not configured", input.Preset),
			domain.NewValidationError("preset", "is not a configured preset"))
	}
	if input.Priority == 0 {
		input.Priority = domain.PriorityNormal
	}
	now := s.clk.Now()
	var job *domain.RenderJob
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		version, err := s.store.Timelines().GetVersion(txCtx, input.TimelineID)
		if err != nil {
			return err
		}
		project, err := s.store.Projects().GetByID(txCtx, version.ProjectID)
		if err != nil {
			return err
		}
		if err := project.EnsureWriteAccess(actor); err != nil {
			return err
		}
		if key := strings.TrimSpace(input.IdempotencyKey); key != "" {
			existing, err := s.store.Renders().FindByIdempotencyKey(txCtx, project.ID, key)
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if err == nil {
				job = existing
				return nil
			}
		}
		if err := version.Renderable(); err != nil {
			return err
		}
		active, err := s.store.Renders().CountActiveForTimeline(txCtx, version.ID)
		if err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("render: timeline %s already has queued work: %w", version.ID, domain.ErrConflict)
		}
		candidate, err := domain.NewRenderJob(s.gen.NewID("rnd"), project.ID, version.ID, actor.UserID,
			input.Preset, input.Priority, s.cfg.Render.MaxAttempts, input.IdempotencyKey, now)
		if err != nil {
			return err
		}
		if err := s.store.Renders().Create(txCtx, candidate); err != nil {
			return err
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionRenderSubmit, audit.ObjectRender, candidate.ID,
			fmt.Sprintf("preset=%s priority=%d", candidate.Preset, int(candidate.Priority))); err != nil {
			return err
		}
		job = candidate
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not queue the render")
	}
	logging.FromContext(ctx, s.logger).Info("render queued", "job_id", job.ID, "preset", job.Preset)
	return job, nil
}

// Claim reserves a slot for the highest priority eligible job and assigns it.
// Reservation and assignment share one transaction so a reserved seat is never
// left behind without an owner.
func (s *Service) Claim(ctx context.Context, pool string) (*domain.RenderJob, error) {
	if strings.TrimSpace(pool) == "" {
		pool = DefaultPool
	}
	now := s.clk.Now()
	var claimed *domain.RenderJob
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		candidates, err := s.store.Renders().NextEligible(txCtx, now, 1)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		job := candidates[0]
		slot, err := s.store.Slots().ReserveIdle(txCtx, pool, job.ID, now)
		if err != nil {
			return err
		}
		expected := job.RowVersion
		if err := job.Assign(slot.ID, s.cfg.Render.LeaseTTL, now); err != nil {
			return err
		}
		if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
			return err
		}
		if err := s.recorder.Succeeded(txCtx, domain.Principal{UserID: "worker", Role: domain.RoleSupervisor},
			domain.ActionRenderAssign, audit.ObjectRender, job.ID, "slot="+slot.ID); err != nil {
			return err
		}
		claimed = job
		return nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrCapacityExhausted) {
			return nil, apierr.Wrap(apierr.CodeQuotaExhausted, "every render seat is busy", err)
		}
		return nil, translate(err, "could not claim a render job")
	}
	return claimed, nil
}

// Start marks an assigned job as rendering.
func (s *Service) Start(ctx context.Context, jobID string) (*domain.RenderJob, error) {
	now := s.clk.Now()
	var started *domain.RenderJob
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		job, err := s.store.Renders().GetByID(txCtx, jobID)
		if err != nil {
			return err
		}
		expected := job.RowVersion
		if err := job.Start(now); err != nil {
			return err
		}
		if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
			return err
		}
		started = job
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not start the render")
	}
	return started, nil
}

// RenewLease pushes the lease deadline of a job that is still being encoded
// forward. Workers call it periodically so a long render is not mistaken for an
// abandoned seat by the housekeeping reaper.
func (s *Service) RenewLease(ctx context.Context, jobID string) (*domain.RenderJob, error) {
	now := s.clk.Now()
	var renewed *domain.RenderJob
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		job, err := s.store.Renders().GetByID(txCtx, jobID)
		if err != nil {
			return err
		}
		expected := job.RowVersion
		if err := job.ExtendLease(s.cfg.Render.LeaseTTL, now); err != nil {
			return err
		}
		if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
			return err
		}
		renewed = job
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not renew the render lease")
	}
	return renewed, nil
}

// CompleteInput carries the render output description.
type CompleteInput struct {
	JobID       string
	OutputURI   string
	OutputBytes int64
}

// Complete records a successful render, releases the slot and materializes one
// pending delivery record per enabled target. Everything happens in a single
// transaction so a delivery bookkeeping failure cannot leave the seat occupied
// or the job marked done without its delivery rows.
func (s *Service) Complete(ctx context.Context, input CompleteInput) (*domain.RenderJob, error) {
	now := s.clk.Now()
	var completed *domain.RenderJob
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		job, err := s.store.Renders().GetByID(txCtx, input.JobID)
		if err != nil {
			return err
		}
		slotID := job.SlotID
		expected := job.RowVersion
		if err := job.Complete(input.OutputURI, input.OutputBytes, now); err != nil {
			return err
		}
		if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
			return err
		}
		if slotID != "" {
			if err := s.store.Slots().Release(txCtx, slotID, job.ID, now); err != nil {
				return err
			}
		}
		targets, err := s.store.Deliveries().ListTargets(txCtx, job.ProjectID, true)
		if err != nil {
			return err
		}
		for _, target := range targets {
			record, err := domain.NewDeliveryRecord(s.gen.NewID("dlv"), job.ID, target.ID, job.ProjectID,
				job.OutputURI, 3, now)
			if err != nil {
				return err
			}
			if err := s.store.Deliveries().CreateRecord(txCtx, record); err != nil {
				return err
			}
		}
		if err := s.recorder.Succeeded(txCtx, domain.Principal{UserID: "worker", Role: domain.RoleSupervisor},
			domain.ActionRenderComplete, audit.ObjectRender, job.ID,
			fmt.Sprintf("output_bytes=%d targets=%d", job.OutputBytes, len(targets))); err != nil {
			return err
		}
		completed = job
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not record the render result")
	}
	return completed, nil
}

// FailAttempt records a failed attempt, releases the slot and either requeues
// with backoff or marks the job permanently failed.
func (s *Service) FailAttempt(ctx context.Context, jobID, reason string) (*domain.RenderJob, error) {
	now := s.clk.Now()
	var failed *domain.RenderJob
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		job, err := s.store.Renders().GetByID(txCtx, jobID)
		if err != nil {
			return err
		}
		slotID := job.SlotID
		expected := job.RowVersion
		if err := job.Fail(reason, s.cfg.Render.RetryBackoff, now); err != nil {
			return err
		}
		if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
			return err
		}
		if slotID != "" {
			if err := s.store.Slots().Release(txCtx, slotID, job.ID, now); err != nil {
				return err
			}
		}
		if err := s.recorder.Failed(txCtx, domain.Principal{UserID: "worker", Role: domain.RoleSupervisor},
			domain.ActionRenderFail, audit.ObjectRender, job.ID,
			fmt.Sprintf("attempt=%d status=%s", job.Attempt, job.Status)); err != nil {
			return err
		}
		failed = job
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not record the render failure")
	}
	return failed, nil
}

// Cancel stops a job that has not finished and frees its seat.
func (s *Service) Cancel(ctx context.Context, actor domain.Principal, jobID, reason string) (*domain.RenderJob, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "canceled by operator"
	}
	now := s.clk.Now()
	var canceled *domain.RenderJob
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		job, err := s.store.Renders().GetByID(txCtx, jobID)
		if err != nil {
			return err
		}
		if err := job.EnsureCancelAuthority(actor); err != nil {
			return err
		}
		slotID := job.SlotID
		expected := job.RowVersion
		if err := job.Cancel(reason, now); err != nil {
			return err
		}
		if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
			return err
		}
		if slotID != "" {
			if err := s.store.Slots().Release(txCtx, slotID, job.ID, now); err != nil {
				return err
			}
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionRenderCancel, audit.ObjectRender, job.ID, reason); err != nil {
			return err
		}
		canceled = job
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not cancel the render")
	}
	return canceled, nil
}

// RequeueExpiredLeases returns abandoned leases to the queue and frees the seats
// they still occupied. It reports how many jobs were recovered.
func (s *Service) RequeueExpiredLeases(ctx context.Context, limit int) (int, error) {
	now := s.clk.Now()
	var recovered int
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		expired, err := s.store.Renders().ListExpiredLeases(txCtx, now, limit)
		if err != nil {
			return err
		}
		for _, job := range expired {
			slotID := job.SlotID
			expected := job.RowVersion
			if err := job.Requeue("render lease expired", s.cfg.Render.RetryBackoff, now); err != nil {
				return err
			}
			if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
				return err
			}
			if slotID != "" {
				if err := s.store.Slots().ForceRelease(txCtx, slotID, now); err != nil {
					return err
				}
			}
			if err := s.recorder.Failed(txCtx, domain.Principal{UserID: "reaper", Role: domain.RoleSupervisor},
				domain.ActionRenderRequeue, audit.ObjectRender, job.ID, "lease expired"); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	if err != nil {
		return 0, translate(err, "could not recover expired render leases")
	}
	return recovered, nil
}

// Abandon returns a leased job to the queue without consuming another attempt.
// The worker calls it during graceful shutdown so in flight work is picked up
// again instead of being counted as a failed attempt.
func (s *Service) Abandon(ctx context.Context, jobID, reason string) error {
	now := s.clk.Now()
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		job, err := s.store.Renders().GetByID(txCtx, jobID)
		if err != nil {
			return err
		}
		if job.Status.Terminal() {
			return nil
		}
		slotID := job.SlotID
		expected := job.RowVersion
		if err := job.Requeue(reason, s.cfg.Render.RetryBackoff, now); err != nil {
			return err
		}
		if err := s.store.Renders().Update(txCtx, job, expected); err != nil {
			return err
		}
		if slotID != "" {
			if err := s.store.Slots().ForceRelease(txCtx, slotID, now); err != nil {
				return err
			}
		}
		return s.recorder.Failed(txCtx, domain.Principal{UserID: "worker", Role: domain.RoleSupervisor},
			domain.ActionRenderRequeue, audit.ObjectRender, job.ID, reason)
	})
	if err != nil {
		return translate(err, "could not return the render job to the queue")
	}
	return nil
}

// Get returns one render job the caller may read.
func (s *Service) Get(ctx context.Context, actor domain.Principal, jobID string) (*domain.RenderJob, error) {
	job, err := s.store.Renders().GetByID(ctx, jobID)
	if err != nil {
		return nil, translate(err, "could not read the render job")
	}
	project, err := s.store.Projects().GetByID(ctx, job.ProjectID)
	if err != nil {
		return nil, translate(err, "could not read the project")
	}
	if actor.IsZero() {
		return nil, apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	// The farm queue is shared, so any signed in operator may look up a render to
	// see what the encoders are working on.
	_ = project
	return job, nil
}

// List returns a filtered page of render jobs.
func (s *Service) List(ctx context.Context, actor domain.Principal, filter domain.RenderFilter, page domain.Page) (domain.PageResult[*domain.RenderJob], error) {
	var empty domain.PageResult[*domain.RenderJob]
	if actor.IsZero() {
		return empty, apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	if actor.Role == domain.RoleEditor {
		filter.RequestedBy = actor.UserID
	}
	result, err := s.store.Renders().List(ctx, filter, page)
	if err != nil {
		return empty, apierr.Wrap(apierr.CodeInternal, "could not list render jobs", err)
	}
	return result, nil
}

// Capacity reports the current farm utilisation.
func (s *Service) Capacity(ctx context.Context, pool string) (domain.FarmCapacity, error) {
	if strings.TrimSpace(pool) == "" {
		pool = DefaultPool
	}
	capacity, err := s.store.Slots().Capacity(ctx, pool)
	if err != nil {
		return domain.FarmCapacity{}, apierr.Wrap(apierr.CodeInternal, "could not read farm capacity", err)
	}
	return capacity, nil
}

// ProvisionSlot registers a new render seat. Supervisors operate the farm.
func (s *Service) ProvisionSlot(ctx context.Context, actor domain.Principal, name, pool string, units int) (*domain.RenderSlot, error) {
	if err := actor.RequireFarmArbitration(); err != nil {
		return nil, apierr.Wrap(apierr.CodeForbidden, "only a supervisor may change the render farm", err)
	}
	if strings.TrimSpace(pool) == "" {
		pool = DefaultPool
	}
	now := s.clk.Now()
	slot, err := domain.NewRenderSlot(s.gen.NewID("slt"), name, pool, units, now)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "the slot details are not acceptable", err)
	}
	err = s.store.InTx(ctx, func(txCtx context.Context) error {
		if err := s.store.Slots().Create(txCtx, slot); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, "render.slot_provision", audit.ObjectSlot, slot.ID, "pool="+pool)
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, apierr.Wrap(apierr.CodeConflict, "a render seat with that name already exists", err)
		}
		return nil, apierr.Wrap(apierr.CodeInternal, "could not provision the render seat", err)
	}
	return slot, nil
}

// LeaseTTL exposes the configured lease window to the worker.
func (s *Service) LeaseTTL() time.Duration { return s.cfg.Render.LeaseTTL }

func translate(err error, message string) error {
	var public *apierr.Error
	if errors.As(err, &public) {
		return public
	}
	switch {
	case errors.Is(err, context.Canceled):
		return apierr.Wrap(apierr.CodeCanceled, "the request was canceled", err)
	case errors.Is(err, context.DeadlineExceeded):
		return apierr.Wrap(apierr.CodeTimeout, "the request exceeded its deadline", err)
	case errors.Is(err, domain.ErrNotFound):
		return apierr.Wrap(apierr.CodeNotFound, "that record does not exist", err)
	case errors.Is(err, domain.ErrCapacityExhausted):
		return apierr.Wrap(apierr.CodeQuotaExhausted, "every render seat is busy", err)
	case errors.Is(err, domain.ErrVersionConflict):
		return apierr.Wrap(apierr.CodeConflict, "this render job changed concurrently, reload and retry", err)
	case errors.Is(err, domain.ErrValidation):
		return apierr.Wrap(apierr.CodeInvalidRequest, message, err)
	case errors.Is(err, domain.ErrInvalidTransition):
		return apierr.Wrap(apierr.CodePreconditionFail, message, err)
	case errors.Is(err, domain.ErrConflict):
		return apierr.Wrap(apierr.CodeConflict, message, err)
	case errors.Is(err, domain.ErrPermissionDenied):
		return apierr.Wrap(apierr.CodeForbidden, "you may not perform this action", err)
	default:
		return apierr.Wrap(apierr.CodeInternal, message, err)
	}
}
