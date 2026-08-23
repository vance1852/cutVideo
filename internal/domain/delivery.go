package domain

import (
	"strings"
	"time"
)

// DeliveryTargetStatus enumerates the availability of a distribution endpoint.
type DeliveryTargetStatus string

// Delivery target statuses.
const (
	TargetEnabled  DeliveryTargetStatus = "enabled"
	TargetDisabled DeliveryTargetStatus = "disabled"
)

// DeliveryKind enumerates supported distribution protocols.
type DeliveryKind string

// Delivery kinds.
const (
	DeliveryWebhook DeliveryKind = "webhook"
	DeliveryS3      DeliveryKind = "s3"
	DeliveryAspera  DeliveryKind = "aspera"
)

// Valid reports whether the delivery kind is known.
func (k DeliveryKind) Valid() bool {
	switch k {
	case DeliveryWebhook, DeliveryS3, DeliveryAspera:
		return true
	default:
		return false
	}
}

// DeliveryTarget is one downstream destination for a finished render.
type DeliveryTarget struct {
	ID            string
	ProjectID     string
	Name          string
	Kind          DeliveryKind
	Endpoint      string
	CredentialRef string
	Status        DeliveryTargetStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewDeliveryTarget validates and builds an enabled delivery target.
func NewDeliveryTarget(id, projectID, name string, kind DeliveryKind, endpoint, credentialRef string, now time.Time) (*DeliveryTarget, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, NewValidationError("project_id", "must not be empty")
	}
	if strings.TrimSpace(name) == "" {
		return nil, NewValidationError("name", "must not be empty")
	}
	if !kind.Valid() {
		return nil, NewValidationError("kind", "must be webhook, s3 or aspera")
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, NewValidationError("endpoint", "must not be empty")
	}
	if strings.TrimSpace(credentialRef) == "" {
		return nil, NewValidationError("credential_ref", "must not be empty")
	}
	return &DeliveryTarget{
		ID:            id,
		ProjectID:     strings.TrimSpace(projectID),
		Name:          strings.TrimSpace(name),
		Kind:          kind,
		Endpoint:      strings.TrimSpace(endpoint),
		CredentialRef: strings.TrimSpace(credentialRef),
		Status:        TargetEnabled,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// Enabled reports whether the target accepts dispatches.
func (t *DeliveryTarget) Enabled() bool { return t.Status == TargetEnabled }

// Disable stops future dispatches to the target.
func (t *DeliveryTarget) Disable(now time.Time) error {
	if t.Status == TargetDisabled {
		return NewTransitionError("delivery_target", string(t.Status), string(TargetDisabled), "already disabled")
	}
	t.Status = TargetDisabled
	t.UpdatedAt = now
	return nil
}

// Enable resumes dispatches to the target.
func (t *DeliveryTarget) Enable(now time.Time) error {
	if t.Status == TargetEnabled {
		return NewTransitionError("delivery_target", string(t.Status), string(TargetEnabled), "already enabled")
	}
	t.Status = TargetEnabled
	t.UpdatedAt = now
	return nil
}

// EnsureManageable reports whether the principal may change this destination.
// Adding, enabling or disabling destinations is a supervisor responsibility;
// editors may only read the list and dispatch their own finished renders.
func (t *DeliveryTarget) EnsureManageable(principal Principal) error {
	if principal.IsZero() {
		return ErrPermissionDenied
	}
	return principal.RequireDeliveryManagement()
}

// Clone returns an independent copy of the target.
func (t *DeliveryTarget) Clone() *DeliveryTarget {
	if t == nil {
		return nil
	}
	copied := *t
	return &copied
}

// DeliveryStatus enumerates the lifecycle of one dispatch attempt record.
type DeliveryStatus string

// Delivery record statuses.
const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryDispatched DeliveryStatus = "dispatched"
	DeliveryConfirmed DeliveryStatus = "confirmed"
	DeliveryFailed    DeliveryStatus = "failed"
	DeliverySkipped   DeliveryStatus = "skipped"
)

// DeliveryRecord tracks one render output on its way to one target.
type DeliveryRecord struct {
	ID           string
	JobID        string
	TargetID     string
	ProjectID    string
	Status       DeliveryStatus
	Attempt      int
	MaxAttempts  int
	OutputURI    string
	Failure      string
	DispatchedAt *time.Time
	ConfirmedAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewDeliveryRecord validates and builds a pending delivery record.
func NewDeliveryRecord(id, jobID, targetID, projectID, outputURI string, maxAttempts int, now time.Time) (*DeliveryRecord, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(jobID) == "" {
		return nil, NewValidationError("job_id", "must not be empty")
	}
	if strings.TrimSpace(targetID) == "" {
		return nil, NewValidationError("target_id", "must not be empty")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, NewValidationError("project_id", "must not be empty")
	}
	if strings.TrimSpace(outputURI) == "" {
		return nil, NewValidationError("output_uri", "must not be empty")
	}
	if maxAttempts < 1 {
		return nil, NewValidationError("max_attempts", "must be at least 1")
	}
	return &DeliveryRecord{
		ID:          id,
		JobID:       strings.TrimSpace(jobID),
		TargetID:    strings.TrimSpace(targetID),
		ProjectID:   strings.TrimSpace(projectID),
		Status:      DeliveryPending,
		MaxAttempts: maxAttempts,
		OutputURI:   strings.TrimSpace(outputURI),
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// MarkDispatched records that the payload left the platform.
func (r *DeliveryRecord) MarkDispatched(now time.Time) error {
	if r.Status != DeliveryPending && r.Status != DeliveryFailed {
		return NewTransitionError("delivery", string(r.Status), string(DeliveryDispatched), "only pending or failed deliveries can be dispatched")
	}
	if r.Attempt >= r.MaxAttempts {
		return NewTransitionError("delivery", string(r.Status), string(DeliveryDispatched), "delivery attempts are exhausted")
	}
	stamp := now
	r.Status = DeliveryDispatched
	r.Attempt++
	r.DispatchedAt = &stamp
	r.Failure = ""
	r.UpdatedAt = now
	return nil
}

// Confirm records the downstream acknowledgement.
func (r *DeliveryRecord) Confirm(now time.Time) error {
	if r.Status != DeliveryDispatched {
		return NewTransitionError("delivery", string(r.Status), string(DeliveryConfirmed), "only dispatched deliveries can be confirmed")
	}
	stamp := now
	r.Status = DeliveryConfirmed
	r.ConfirmedAt = &stamp
	r.UpdatedAt = now
	return nil
}

// MarkFailed records a dispatch failure.
func (r *DeliveryRecord) MarkFailed(reason string, now time.Time) error {
	if r.Status == DeliveryConfirmed {
		return NewTransitionError("delivery", string(r.Status), string(DeliveryFailed), "confirmed deliveries cannot fail")
	}
	if strings.TrimSpace(reason) == "" {
		return NewValidationError("reason", "must not be empty")
	}
	r.Status = DeliveryFailed
	r.Failure = strings.TrimSpace(reason)
	r.UpdatedAt = now
	return nil
}

// Skip records that the target was disabled at dispatch time.
func (r *DeliveryRecord) Skip(reason string, now time.Time) error {
	if r.Status != DeliveryPending {
		return NewTransitionError("delivery", string(r.Status), string(DeliverySkipped), "only pending deliveries can be skipped")
	}
	r.Status = DeliverySkipped
	r.Failure = strings.TrimSpace(reason)
	r.UpdatedAt = now
	return nil
}

// Exhausted reports whether no further dispatch attempt is allowed.
func (r *DeliveryRecord) Exhausted() bool { return r.Attempt >= r.MaxAttempts }

// Clone returns an independent copy of the record.
func (r *DeliveryRecord) Clone() *DeliveryRecord {
	if r == nil {
		return nil
	}
	copied := *r
	if r.DispatchedAt != nil {
		stamp := *r.DispatchedAt
		copied.DispatchedAt = &stamp
	}
	if r.ConfirmedAt != nil {
		stamp := *r.ConfirmedAt
		copied.ConfirmedAt = &stamp
	}
	return &copied
}

// DeliveryOutcome is one entry of a batch dispatch result.
type DeliveryOutcome struct {
	TargetID   string
	TargetName string
	Status     DeliveryStatus
	Failure    string
}

// BatchDeliveryResult summarizes a partial failure batch dispatch.
type BatchDeliveryResult struct {
	JobID     string
	Requested int
	Confirmed int
	Failed    int
	Skipped   int
	Outcomes  []DeliveryOutcome
}

// FullyConfirmed reports whether every requested target acknowledged.
func (b BatchDeliveryResult) FullyConfirmed() bool {
	return b.Requested > 0 && b.Confirmed == b.Requested
}
