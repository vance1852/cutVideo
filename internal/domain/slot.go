package domain

import (
	"strings"
	"time"
)

// SlotStatus enumerates render slot availability.
type SlotStatus string

// Slot statuses.
const (
	SlotIdle     SlotStatus = "idle"
	SlotBusy     SlotStatus = "busy"
	SlotDraining SlotStatus = "draining"
	SlotOffline  SlotStatus = "offline"
)

// RenderSlot is one exclusive encoder seat in the shared render farm. A slot may
// be held by at most one render job at a time; the invariant is enforced with a
// conditional update in the relational store.
type RenderSlot struct {
	ID          string
	Name        string
	Pool        string
	Units       int
	Status      SlotStatus
	HeldByJobID string
	LeasedAt    *time.Time
	ReleasedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewRenderSlot validates and builds an idle slot.
func NewRenderSlot(id, name, pool string, units int, now time.Time) (*RenderSlot, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(name) == "" {
		return nil, NewValidationError("name", "must not be empty")
	}
	if strings.TrimSpace(pool) == "" {
		return nil, NewValidationError("pool", "must not be empty")
	}
	if units < 1 {
		return nil, NewValidationError("units", "must be at least 1")
	}
	return &RenderSlot{
		ID:        id,
		Name:      strings.TrimSpace(name),
		Pool:      strings.TrimSpace(pool),
		Units:     units,
		Status:    SlotIdle,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Available reports whether the slot can accept new work.
func (s *RenderSlot) Available() bool { return s.Status == SlotIdle && s.HeldByJobID == "" }

// Reserve binds the slot to a render job.
func (s *RenderSlot) Reserve(jobID string, now time.Time) error {
	if strings.TrimSpace(jobID) == "" {
		return NewValidationError("job_id", "must not be empty")
	}
	if !s.Available() {
		return NewTransitionError("render_slot", string(s.Status), string(SlotBusy), "slot is not available")
	}
	stamp := now
	s.Status = SlotBusy
	s.HeldByJobID = strings.TrimSpace(jobID)
	s.LeasedAt = &stamp
	s.ReleasedAt = nil
	s.UpdatedAt = now
	return nil
}

// Release returns the slot to the pool. Draining slots go offline instead of
// becoming available again.
func (s *RenderSlot) Release(jobID string, now time.Time) error {
	if s.Status != SlotBusy {
		return NewTransitionError("render_slot", string(s.Status), string(SlotIdle), "only busy slots can be released")
	}
	if strings.TrimSpace(jobID) != "" && s.HeldByJobID != strings.TrimSpace(jobID) {
		return ErrConflict
	}
	stamp := now
	s.HeldByJobID = ""
	s.ReleasedAt = &stamp
	s.LeasedAt = nil
	s.UpdatedAt = now
	s.Status = SlotIdle
	return nil
}

// Drain prevents new work from landing on the slot.
func (s *RenderSlot) Drain(now time.Time) error {
	switch s.Status {
	case SlotIdle:
		s.Status = SlotOffline
		s.UpdatedAt = now
		return nil
	case SlotBusy:
		s.Status = SlotDraining
		s.UpdatedAt = now
		return nil
	default:
		return NewTransitionError("render_slot", string(s.Status), string(SlotDraining), "slot cannot be drained")
	}
}

// Activate brings an offline slot back into the pool.
func (s *RenderSlot) Activate(now time.Time) error {
	if s.Status != SlotOffline {
		return NewTransitionError("render_slot", string(s.Status), string(SlotIdle), "only offline slots can be activated")
	}
	s.Status = SlotIdle
	s.HeldByJobID = ""
	s.UpdatedAt = now
	return nil
}

// Clone returns an independent copy of the slot.
func (s *RenderSlot) Clone() *RenderSlot {
	if s == nil {
		return nil
	}
	copied := *s
	if s.LeasedAt != nil {
		stamp := *s.LeasedAt
		copied.LeasedAt = &stamp
	}
	if s.ReleasedAt != nil {
		stamp := *s.ReleasedAt
		copied.ReleasedAt = &stamp
	}
	return &copied
}

// FarmCapacity summarizes the render farm for the readiness endpoint.
type FarmCapacity struct {
	Pool      string
	Total     int
	Idle      int
	Busy      int
	Draining  int
	Offline   int
	QueuedJob int
}

// Saturated reports whether every usable slot is occupied.
func (c FarmCapacity) Saturated() bool { return c.Idle == 0 }
