package domain

import (
	"strings"
	"time"
)

// RenderStatus enumerates the render job state machine.
type RenderStatus string

// Render job statuses.
const (
	RenderQueued    RenderStatus = "queued"
	RenderAssigned  RenderStatus = "assigned"
	RenderRendering RenderStatus = "rendering"
	RenderSucceeded RenderStatus = "succeeded"
	RenderFailed    RenderStatus = "failed"
	RenderCanceled  RenderStatus = "canceled"
)

// Terminal reports whether no further transition is possible.
func (s RenderStatus) Terminal() bool {
	switch s {
	case RenderSucceeded, RenderFailed, RenderCanceled:
		return true
	default:
		return false
	}
}

// RenderPriority enumerates queue priorities.
type RenderPriority int

// Render priorities.
const (
	PriorityBackground RenderPriority = 10
	PriorityNormal     RenderPriority = 50
	PriorityUrgent     RenderPriority = 90
)

// Valid reports whether the priority is a known band.
func (p RenderPriority) Valid() bool {
	switch p {
	case PriorityBackground, PriorityNormal, PriorityUrgent:
		return true
	default:
		return false
	}
}

// RenderJob is one unit of render farm work for a sealed timeline version.
type RenderJob struct {
	ID             string
	ProjectID      string
	TimelineID     string
	RequestedBy    string
	Preset         string
	Priority       RenderPriority
	Status         RenderStatus
	Attempt        int
	MaxAttempts    int
	SlotID         string
	LeaseExpiresAt *time.Time
	NextAttemptAt  time.Time
	QueuedAt       time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	LastError      string
	OutputURI      string
	OutputBytes    int64
	IdempotencyKey string
	RowVersion     int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewRenderJob validates and builds a queued render job.
func NewRenderJob(id, projectID, timelineID, requestedBy, preset string, priority RenderPriority, maxAttempts int, idempotencyKey string, now time.Time) (*RenderJob, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, NewValidationError("project_id", "must not be empty")
	}
	if strings.TrimSpace(timelineID) == "" {
		return nil, NewValidationError("timeline_id", "must not be empty")
	}
	if strings.TrimSpace(requestedBy) == "" {
		return nil, NewValidationError("requested_by", "must not be empty")
	}
	if strings.TrimSpace(preset) == "" {
		return nil, NewValidationError("preset", "must not be empty")
	}
	if !priority.Valid() {
		return nil, NewValidationError("priority", "must be 10, 50 or 90")
	}
	if maxAttempts < 1 {
		return nil, NewValidationError("max_attempts", "must be at least 1")
	}
	return &RenderJob{
		ID:             id,
		ProjectID:      strings.TrimSpace(projectID),
		TimelineID:     strings.TrimSpace(timelineID),
		RequestedBy:    strings.TrimSpace(requestedBy),
		Preset:         strings.ToLower(strings.TrimSpace(preset)),
		Priority:       priority,
		Status:         RenderQueued,
		Attempt:        0,
		MaxAttempts:    maxAttempts,
		NextAttemptAt:  now,
		QueuedAt:       now,
		IdempotencyKey: strings.TrimSpace(idempotencyKey),
		RowVersion:     1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Active reports whether the job still occupies queue or farm resources.
func (j *RenderJob) Active() bool { return !j.Status.Terminal() }

// HoldsSlot reports whether the job currently owns a render slot.
func (j *RenderJob) HoldsSlot() bool { return strings.TrimSpace(j.SlotID) != "" }

// Eligible reports whether the queued job may be picked up at the given time.
func (j *RenderJob) Eligible(now time.Time) bool {
	return j.Status == RenderQueued && !now.Before(j.NextAttemptAt)
}

// Assign binds a reserved render slot to the job and starts the lease.
func (j *RenderJob) Assign(slotID string, leaseTTL time.Duration, now time.Time) error {
	if j.Status != RenderQueued {
		return NewTransitionError("render_job", string(j.Status), string(RenderAssigned), "only queued jobs can be assigned")
	}
	if strings.TrimSpace(slotID) == "" {
		return NewValidationError("slot_id", "must not be empty")
	}
	if leaseTTL <= 0 {
		return NewValidationError("lease_ttl", "must be positive")
	}
	if now.Before(j.NextAttemptAt) {
		return NewTransitionError("render_job", string(j.Status), string(RenderAssigned), "backoff window has not elapsed")
	}
	expiry := now.Add(leaseTTL)
	j.Status = RenderAssigned
	j.SlotID = strings.TrimSpace(slotID)
	j.LeaseExpiresAt = &expiry
	j.Attempt++
	j.RowVersion++
	j.UpdatedAt = now
	return nil
}

// Start moves an assigned job into rendering.
func (j *RenderJob) Start(now time.Time) error {
	if j.Status != RenderAssigned {
		return NewTransitionError("render_job", string(j.Status), string(RenderRendering), "only assigned jobs can start rendering")
	}
	if !j.HoldsSlot() {
		return NewTransitionError("render_job", string(j.Status), string(RenderRendering), "a render slot must be held before rendering")
	}
	stamp := now
	j.Status = RenderRendering
	j.StartedAt = &stamp
	j.RowVersion++
	j.UpdatedAt = now
	return nil
}

// ExtendLease pushes the lease deadline forward while work continues.
func (j *RenderJob) ExtendLease(leaseTTL time.Duration, now time.Time) error {
	if j.Status != RenderRendering && j.Status != RenderAssigned {
		return NewTransitionError("render_job", string(j.Status), string(j.Status), "only leased jobs can extend a lease")
	}
	if leaseTTL <= 0 {
		return NewValidationError("lease_ttl", "must be positive")
	}
	expiry := now.Add(leaseTTL)
	j.LeaseExpiresAt = &expiry
	j.RowVersion++
	j.UpdatedAt = now
	return nil
}

// LeaseExpired reports whether the current lease elapsed.
func (j *RenderJob) LeaseExpired(now time.Time) bool {
	if j.LeaseExpiresAt == nil {
		return false
	}
	if j.Status != RenderAssigned && j.Status != RenderRendering {
		return false
	}
	return !now.Before(*j.LeaseExpiresAt)
}

// Complete records a successful render and releases the slot reference.
func (j *RenderJob) Complete(outputURI string, outputBytes int64, now time.Time) error {
	if j.Status != RenderRendering {
		return NewTransitionError("render_job", string(j.Status), string(RenderSucceeded), "only rendering jobs can succeed")
	}
	if strings.TrimSpace(outputURI) == "" {
		return NewValidationError("output_uri", "must not be empty")
	}
	if outputBytes <= 0 {
		return NewValidationError("output_bytes", "must be positive")
	}
	stamp := now
	j.Status = RenderSucceeded
	j.OutputURI = strings.TrimSpace(outputURI)
	j.OutputBytes = outputBytes
	j.FinishedAt = &stamp
	j.SlotID = ""
	j.LeaseExpiresAt = nil
	j.LastError = ""
	j.RowVersion++
	j.UpdatedAt = now
	return nil
}

// Fail records an attempt failure. The job returns to the queue with backoff
// while attempts remain, and becomes permanently failed once they are used up.
func (j *RenderJob) Fail(reason string, backoff time.Duration, now time.Time) error {
	if j.Status != RenderRendering && j.Status != RenderAssigned {
		return NewTransitionError("render_job", string(j.Status), string(RenderFailed), "only leased jobs can fail an attempt")
	}
	if strings.TrimSpace(reason) == "" {
		return NewValidationError("reason", "must not be empty")
	}
	j.LastError = strings.TrimSpace(reason)
	j.SlotID = ""
	j.LeaseExpiresAt = nil
	j.RowVersion++
	j.UpdatedAt = now
	if j.Attempt >= j.MaxAttempts {
		stamp := now
		j.Status = RenderFailed
		j.FinishedAt = &stamp
		return nil
	}
	j.Status = RenderQueued
	j.NextAttemptAt = now.Add(backoff * time.Duration(j.Attempt))
	j.StartedAt = nil
	return nil
}

// Requeue returns an abandoned lease to the queue without consuming a further
// attempt beyond the one already counted.
func (j *RenderJob) Requeue(reason string, backoff time.Duration, now time.Time) error {
	if j.Status != RenderAssigned && j.Status != RenderRendering {
		return NewTransitionError("render_job", string(j.Status), string(RenderQueued), "only leased jobs can be requeued")
	}
	j.Status = RenderQueued
	j.SlotID = ""
	j.LeaseExpiresAt = nil
	j.StartedAt = nil
	j.LastError = strings.TrimSpace(reason)
	j.NextAttemptAt = now.Add(backoff)
	j.RowVersion++
	j.UpdatedAt = now
	return nil
}

// Cancel stops a job that has not finished yet.
func (j *RenderJob) Cancel(reason string, now time.Time) error {
	if j.Status.Terminal() {
		return NewTransitionError("render_job", string(j.Status), string(RenderCanceled), "finished jobs cannot be canceled")
	}
	stamp := now
	j.Status = RenderCanceled
	j.LastError = strings.TrimSpace(reason)
	j.FinishedAt = &stamp
	j.SlotID = ""
	j.LeaseExpiresAt = nil
	j.RowVersion++
	j.UpdatedAt = now
	return nil
}

// EnsureCancelAuthority allows only the requester or a supervisor to cancel.
// Other editors, regardless of whether they can see the render in the queue,
// must not stop work that they did not submit.
func (j *RenderJob) EnsureCancelAuthority(principal Principal) error {
	if principal.IsZero() {
		return ErrPermissionDenied
	}
	if j.RequestedBy == principal.UserID {
		return principal.RequireEdit()
	}
	return principal.RequireFarmArbitration()
}

// Clone returns an independent copy of the job.
func (j *RenderJob) Clone() *RenderJob {
	if j == nil {
		return nil
	}
	copied := *j
	if j.LeaseExpiresAt != nil {
		stamp := *j.LeaseExpiresAt
		copied.LeaseExpiresAt = &stamp
	}
	if j.StartedAt != nil {
		stamp := *j.StartedAt
		copied.StartedAt = &stamp
	}
	if j.FinishedAt != nil {
		stamp := *j.FinishedAt
		copied.FinishedAt = &stamp
	}
	return &copied
}

// RenderFilter narrows a render job listing.
type RenderFilter struct {
	ProjectID   string
	TimelineID  string
	Status      RenderStatus
	Preset      string
	RequestedBy string
	OnlyActive  bool
}
