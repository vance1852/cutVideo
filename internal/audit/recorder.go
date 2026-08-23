// Package audit turns business outcomes into durable audit events. Every event
// binds the actor, the object, the result and the originating request so an
// operator can reconstruct who changed what.
package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/repository"
)

// Object kinds used in audit events.
const (
	ObjectProject  = "project"
	ObjectAsset    = "media_asset"
	ObjectTimeline = "timeline_version"
	ObjectRender   = "render_job"
	ObjectDelivery = "delivery"
	ObjectSession  = "session"
	ObjectSlot     = "render_slot"
)

// Recorder appends audit events inside the caller's transaction.
type Recorder struct {
	repo   repository.AuditRepository
	gen    ids.Generator
	clk    clock.Clock
	logger *slog.Logger
}

// NewRecorder builds a recorder.
func NewRecorder(repo repository.AuditRepository, gen ids.Generator, clk clock.Clock, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Recorder{repo: repo, gen: gen, clk: clk, logger: logger}
}

// Entry describes one audit event to append.
type Entry struct {
	Actor      domain.Principal
	Action     string
	ObjectKind string
	ObjectID   string
	Result     domain.AuditResult
	Detail     string
}

// Record appends the entry. A failure to record is returned to the caller so a
// transactional business action can roll back rather than silently losing the
// audit trail.
func (r *Recorder) Record(ctx context.Context, entry Entry) error {
	event, err := domain.NewAuditEvent(
		r.gen.NewID("aud"),
		entry.Actor.UserID,
		entry.Actor.Role,
		entry.Action,
		entry.ObjectKind,
		entry.ObjectID,
		entry.Result,
		logging.RequestID(ctx),
		entry.Detail,
		r.clk.Now(),
	)
	if err != nil {
		return fmt.Errorf("audit: build event for %s: %w", entry.Action, err)
	}
	if err := r.repo.Append(ctx, event); err != nil {
		return fmt.Errorf("audit: append %s: %w", entry.Action, err)
	}
	logging.FromContext(ctx, r.logger).Debug("audit event recorded",
		"action", event.Action,
		"object_kind", event.ObjectKind,
		"object_id", event.ObjectID,
		"result", string(event.Result),
	)
	return nil
}

// Succeeded records a successful action.
func (r *Recorder) Succeeded(ctx context.Context, actor domain.Principal, action, objectKind, objectID, detail string) error {
	return r.Record(ctx, Entry{
		Actor:      actor,
		Action:     action,
		ObjectKind: objectKind,
		ObjectID:   objectID,
		Result:     domain.AuditSucceeded,
		Detail:     detail,
	})
}

// Rejected records a business rule rejection.
func (r *Recorder) Rejected(ctx context.Context, actor domain.Principal, action, objectKind, objectID, detail string) error {
	return r.Record(ctx, Entry{
		Actor:      actor,
		Action:     action,
		ObjectKind: objectKind,
		ObjectID:   objectID,
		Result:     domain.AuditRejected,
		Detail:     detail,
	})
}

// Failed records an infrastructure failure.
func (r *Recorder) Failed(ctx context.Context, actor domain.Principal, action, objectKind, objectID, detail string) error {
	return r.Record(ctx, Entry{
		Actor:      actor,
		Action:     action,
		ObjectKind: objectKind,
		ObjectID:   objectID,
		Result:     domain.AuditFailed,
		Detail:     detail,
	})
}

// Query lists audit events for operators.
func (r *Recorder) Query(ctx context.Context, filter domain.AuditFilter, page domain.Page) (domain.PageResult[*domain.AuditEvent], error) {
	return r.repo.List(ctx, filter, page)
}
