// Package delivery owns distribution: which destinations a project publishes to
// and how a finished render reaches each of them.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/audit"
	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/repository"
)

// Service implements the distribution use cases.
type Service struct {
	store     repository.Store
	recorder  *audit.Recorder
	transport Transport
	gen       ids.Generator
	clk       clock.Clock
	logger    *slog.Logger
}

// New builds the delivery service.
func New(store repository.Store, recorder *audit.Recorder, transport Transport, gen ids.Generator, clk clock.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Service{store: store, recorder: recorder, transport: transport, gen: gen, clk: clk, logger: logger}
}

// CreateTargetInput describes a new distribution destination.
type CreateTargetInput struct {
	ProjectID     string
	Name          string
	Kind          domain.DeliveryKind
	Endpoint      string
	CredentialRef string
}

// CreateTarget registers a destination for a project.
func (s *Service) CreateTarget(ctx context.Context, actor domain.Principal, input CreateTargetInput) (*domain.DeliveryTarget, error) {
	if err := actor.RequireDeliveryManagement(); err != nil {
		return nil, apierr.Wrap(apierr.CodeForbidden, "only a supervisor may manage destinations", err)
	}
	now := s.clk.Now()
	target, err := domain.NewDeliveryTarget(s.gen.NewID("dst"), input.ProjectID, input.Name, input.Kind,
		input.Endpoint, input.CredentialRef, now)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "the destination details are not acceptable", err)
	}
	err = s.store.InTx(ctx, func(txCtx context.Context) error {
		if _, err := s.store.Projects().GetByID(txCtx, input.ProjectID); err != nil {
			return err
		}
		if err := s.store.Deliveries().CreateTarget(txCtx, target); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, domain.ActionDeliveryCreate, audit.ObjectDelivery, target.ID,
			"kind="+string(target.Kind))
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, apierr.Wrap(apierr.CodeConflict, "that destination name is already used on this project", err)
		}
		return nil, translate(err, "could not create the destination")
	}
	return target, nil
}

// SetTargetEnabled enables or disables one destination.
func (s *Service) SetTargetEnabled(ctx context.Context, actor domain.Principal, targetID string, enabled bool) (*domain.DeliveryTarget, error) {
	if err := actor.RequireDeliveryManagement(); err != nil {
		return nil, apierr.Wrap(apierr.CodeForbidden, "only a supervisor may manage destinations", err)
	}
	now := s.clk.Now()
	var updated *domain.DeliveryTarget
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		target, err := s.store.Deliveries().GetTarget(txCtx, targetID)
		if err != nil {
			return err
		}
		if enabled {
			if err := target.Enable(now); err != nil {
				return err
			}
		} else {
			if err := target.Disable(now); err != nil {
				return err
			}
		}
		if err := s.store.Deliveries().UpdateTarget(txCtx, target); err != nil {
			return err
		}
		updated = target
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not change the destination")
	}
	return updated, nil
}

// ListTargets returns the destinations of a project.
func (s *Service) ListTargets(ctx context.Context, actor domain.Principal, projectID string, onlyEnabled bool) ([]*domain.DeliveryTarget, error) {
	if actor.IsZero() {
		return nil, apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	project, err := s.store.Projects().GetByID(ctx, projectID)
	if err != nil {
		return nil, translate(err, "could not read the project")
	}
	if !project.OwnedBy(actor) && actor.Role == domain.RoleEditor {
		return nil, apierr.Wrap(apierr.CodeForbidden, "you may not read this project", domain.ErrPermissionDenied)
	}
	targets, err := s.store.Deliveries().ListTargets(ctx, projectID, onlyEnabled)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternal, "could not list destinations", err)
	}
	return targets, nil
}

// Dispatch pushes a finished render to every pending destination. Each record is
// updated independently so one unreachable destination does not discard the
// confirmations already collected.
func (s *Service) Dispatch(ctx context.Context, actor domain.Principal, jobID string) (domain.BatchDeliveryResult, error) {
	var result domain.BatchDeliveryResult
	job, err := s.store.Renders().GetByID(ctx, jobID)
	if err != nil {
		return result, translate(err, "could not read the render job")
	}
	if job.Status != domain.RenderSucceeded {
		return result, apierr.Wrap(apierr.CodePreconditionFail, "only a finished render can be distributed",
			domain.NewTransitionError("render_job", string(job.Status), string(domain.RenderSucceeded), "render is not finished"))
	}
	project, err := s.store.Projects().GetByID(ctx, job.ProjectID)
	if err != nil {
		return result, translate(err, "could not read the project")
	}
	if err := project.EnsureWriteAccess(actor); err != nil {
		return result, apierr.Wrap(apierr.CodeForbidden, "you may not distribute this render", err)
	}
	records, err := s.store.Deliveries().ListRecordsForJob(ctx, jobID)
	if err != nil {
		return result, translate(err, "could not read the delivery records")
	}
	result.JobID = jobID
	for _, record := range records {
		if record.Status == domain.DeliveryConfirmed {
			result.Requested++
			result.Confirmed++
			result.Outcomes = append(result.Outcomes, domain.DeliveryOutcome{
				TargetID: record.TargetID,
				Status:   record.Status,
			})
			continue
		}
		target, err := s.store.Deliveries().GetTarget(ctx, record.TargetID)
		if err != nil {
			return result, translate(err, "could not read the destination")
		}
		result.Requested++
		outcome := s.dispatchOne(ctx, actor, job, target, record)
		switch outcome.Status {
		case domain.DeliveryConfirmed:
			result.Confirmed++
		case domain.DeliverySkipped:
			result.Skipped++
		default:
			result.Failed++
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	if result.FullyConfirmed() {
		if err := s.markProjectDelivered(ctx, actor, project.ID); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Service) dispatchOne(ctx context.Context, actor domain.Principal, job *domain.RenderJob, target *domain.DeliveryTarget, record *domain.DeliveryRecord) domain.DeliveryOutcome {
	now := s.clk.Now()
	outcome := domain.DeliveryOutcome{TargetID: target.ID, TargetName: target.Name}
	if !target.Enabled() {
		if err := s.store.InTx(ctx, func(txCtx context.Context) error {
			if err := record.Skip("destination disabled", now); err != nil {
				return err
			}
			return s.store.Deliveries().UpdateRecord(txCtx, record)
		}); err != nil {
			outcome.Status = domain.DeliveryFailed
			outcome.Failure = "could not mark the destination as skipped"
			return outcome
		}
		outcome.Status = domain.DeliverySkipped
		outcome.Failure = "destination disabled"
		return outcome
	}
	if record.Exhausted() {
		outcome.Status = domain.DeliveryFailed
		outcome.Failure = "delivery attempts are exhausted"
		return outcome
	}
	if err := s.store.InTx(ctx, func(txCtx context.Context) error {
		if err := record.MarkDispatched(now); err != nil {
			return err
		}
		return s.store.Deliveries().UpdateRecord(txCtx, record)
	}); err != nil {
		outcome.Status = domain.DeliveryFailed
		outcome.Failure = "could not mark the delivery as dispatched"
		return outcome
	}

	payload := Payload{
		JobID:      job.ID,
		ProjectID:  job.ProjectID,
		TimelineID: job.TimelineID,
		Preset:     job.Preset,
		OutputURI:  job.OutputURI,
		Bytes:      job.OutputBytes,
		Attempt:    record.Attempt,
		SentAt:     now.Format("2006-01-02T15:04:05Z07:00"),
	}
	answer, sendErr := s.transport.Send(ctx, target, payload)
	txErr := s.store.InTx(ctx, func(txCtx context.Context) error {
		if sendErr != nil {
			if err := record.MarkFailed(sendErr.Error(), s.clk.Now()); err != nil {
				return err
			}
			if err := s.store.Deliveries().UpdateRecord(txCtx, record); err != nil {
				return err
			}
			return s.recorder.Failed(txCtx, actor, domain.ActionDeliveryDispatch, audit.ObjectDelivery, record.ID,
				fmt.Sprintf("target=%s attempt=%d", target.Name, record.Attempt))
		}
		if err := record.Confirm(s.clk.Now()); err != nil {
			return err
		}
		if err := s.store.Deliveries().UpdateRecord(txCtx, record); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, domain.ActionDeliveryDispatch, audit.ObjectDelivery, record.ID,
			fmt.Sprintf("target=%s answer=%s", target.Name, truncate(answer, 64)))
	})
	if txErr != nil {
		outcome.Status = domain.DeliveryFailed
		outcome.Failure = "could not record the delivery result"
		return outcome
	}
	if sendErr != nil {
		outcome.Status = domain.DeliveryFailed
		outcome.Failure = sendErr.Error()
		logging.FromContext(ctx, s.logger).Warn("delivery attempt failed",
			"job_id", job.ID, "target", target.Name, "attempt", record.Attempt)
		return outcome
	}
	outcome.Status = domain.DeliveryConfirmed
	return outcome
}

func (s *Service) markProjectDelivered(ctx context.Context, actor domain.Principal, projectID string) error {
	now := s.clk.Now()
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		project, err := s.store.Projects().GetByID(txCtx, projectID)
		if err != nil {
			return err
		}
		if project.Status == domain.ProjectDelivered {
			return nil
		}
		if err := project.MarkDelivered(now); err != nil {
			return err
		}
		if err := s.store.Projects().Update(txCtx, project); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, "project.delivered", audit.ObjectProject, project.ID, "all destinations confirmed")
	})
	if err != nil && !errors.Is(err, domain.ErrInvalidTransition) {
		return translate(err, "could not mark the project as delivered")
	}
	return nil
}

// ListRecords returns the delivery records of one render job.
func (s *Service) ListRecords(ctx context.Context, actor domain.Principal, jobID string) ([]*domain.DeliveryRecord, error) {
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
	// Distribution progress is farm wide information, so it is readable by any
	// signed in operator.
	_ = project
	records, err := s.store.Deliveries().ListRecordsForJob(ctx, jobID)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternal, "could not list delivery records", err)
	}
	return records, nil
}

func truncate(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}

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
