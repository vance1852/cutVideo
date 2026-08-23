package domain

import (
	"strings"
	"time"
)

// AuditResult enumerates the outcome recorded for an audited action.
type AuditResult string

// Audit results.
const (
	AuditSucceeded AuditResult = "succeeded"
	AuditRejected  AuditResult = "rejected"
	AuditFailed    AuditResult = "failed"
)

// Audited actions. Values are stable and used by operational queries.
const (
	ActionSignIn          = "auth.sign_in"
	ActionSignOut         = "auth.sign_out"
	ActionAssetIngest     = "media.ingest"
	ActionAssetVerify     = "media.verify"
	ActionAssetQuarantine = "media.quarantine"
	ActionProjectCreate   = "project.create"
	ActionProjectLock     = "project.lock"
	ActionTimelineDraft   = "timeline.draft"
	ActionTimelineClipAdd = "timeline.clip_add"
	ActionTimelineSeal    = "timeline.seal"
	ActionRenderSubmit    = "render.submit"
	ActionRenderAssign    = "render.assign"
	ActionRenderComplete  = "render.complete"
	ActionRenderFail      = "render.fail"
	ActionRenderCancel    = "render.cancel"
	ActionRenderRequeue   = "render.requeue"
	ActionDeliveryCreate  = "delivery.target_create"
	ActionDeliveryDispatch = "delivery.dispatch"
)

// AuditEvent is one durable record of a business action.
type AuditEvent struct {
	ID         string
	ActorID    string
	ActorRole  Role
	Action     string
	ObjectKind string
	ObjectID   string
	Result     AuditResult
	RequestID  string
	Detail     string
	CreatedAt  time.Time
}

// NewAuditEvent validates and builds an audit event.
func NewAuditEvent(id, actorID string, role Role, action, objectKind, objectID string, result AuditResult, requestID, detail string, now time.Time) (*AuditEvent, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(action) == "" {
		return nil, NewValidationError("action", "must not be empty")
	}
	if strings.TrimSpace(objectKind) == "" {
		return nil, NewValidationError("object_kind", "must not be empty")
	}
	switch result {
	case AuditSucceeded, AuditRejected, AuditFailed:
	default:
		return nil, NewValidationError("result", "must be succeeded, rejected or failed")
	}
	return &AuditEvent{
		ID:         id,
		ActorID:    strings.TrimSpace(actorID),
		ActorRole:  role,
		Action:     strings.TrimSpace(action),
		ObjectKind: strings.TrimSpace(objectKind),
		ObjectID:   strings.TrimSpace(objectID),
		Result:     result,
		RequestID:  strings.TrimSpace(requestID),
		Detail:     strings.TrimSpace(detail),
		CreatedAt:  now,
	}, nil
}

// Clone returns an independent copy of the event.
func (e *AuditEvent) Clone() *AuditEvent {
	if e == nil {
		return nil
	}
	copied := *e
	return &copied
}

// AuditFilter narrows an audit listing.
type AuditFilter struct {
	ActorID    string
	Action     string
	ObjectKind string
	ObjectID   string
	Result     AuditResult
	Since      *time.Time
}

// IdempotencyRecord stores the first response produced for a mutating request
// so a retried submission is replayed instead of duplicated.
type IdempotencyRecord struct {
	ID           string
	Scope        string
	Method       string
	Path         string
	ActorID      string
	Key          string
	RequestHash  string
	ResponseCode int
	ResponseBody string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// NewIdempotencyRecord validates and builds a record.
func NewIdempotencyRecord(id, scope, method, path, actorID, key, requestHash string, code int, body string, now time.Time, ttl time.Duration) (*IdempotencyRecord, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(scope) == "" {
		return nil, NewValidationError("scope", "must not be empty")
	}
	if strings.TrimSpace(method) == "" {
		return nil, NewValidationError("method", "must not be empty")
	}
	if strings.TrimSpace(path) == "" {
		return nil, NewValidationError("path", "must not be empty")
	}
	if strings.TrimSpace(key) == "" {
		return nil, NewValidationError("idempotency_key", "must not be empty")
	}
	if ttl <= 0 {
		return nil, NewValidationError("ttl", "must be positive")
	}
	return &IdempotencyRecord{
		ID:           id,
		Scope:        strings.TrimSpace(scope),
		Method:       strings.ToUpper(strings.TrimSpace(method)),
		Path:         strings.TrimSpace(path),
		ActorID:      strings.TrimSpace(actorID),
		Key:          strings.TrimSpace(key),
		RequestHash:  strings.TrimSpace(requestHash),
		ResponseCode: code,
		ResponseBody: body,
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}, nil
}

// Expired reports whether the record may be replaced.
func (r *IdempotencyRecord) Expired(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// Matches reports whether a replayed request carries the same payload.
func (r *IdempotencyRecord) Matches(requestHash string) bool {
	return r.RequestHash == strings.TrimSpace(requestHash)
}
