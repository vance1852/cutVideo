// Package domain holds the editorial entities, state machines and invariants of
// the cutVideo render orchestration platform. It depends on neither HTTP nor a
// concrete database driver.
package domain

import "errors"

// Sentinel errors describing business outcomes. Callers compare with errors.Is
// so wrapping is always preserved through the service and storage layers.
var (
	// ErrNotFound is returned when an entity does not exist.
	ErrNotFound = errors.New("domain: entity not found")
	// ErrConflict is returned when a uniqueness or ownership rule is violated.
	ErrConflict = errors.New("domain: conflicting entity state")
	// ErrInvalidTransition is returned when a state machine rejects a move.
	ErrInvalidTransition = errors.New("domain: invalid state transition")
	// ErrValidation is returned when input does not satisfy a business rule.
	ErrValidation = errors.New("domain: validation failed")
	// ErrVersionConflict is returned when an optimistic update loses the race.
	ErrVersionConflict = errors.New("domain: version conflict")
	// ErrCapacityExhausted is returned when no render slot can be reserved.
	ErrCapacityExhausted = errors.New("domain: render capacity exhausted")
	// ErrPermissionDenied is returned when a role may not perform an action.
	ErrPermissionDenied = errors.New("domain: permission denied")
	// ErrCredentialsRejected is returned for a failed sign in attempt.
	ErrCredentialsRejected = errors.New("domain: credentials rejected")
	// ErrSessionExpired is returned when a session is past its expiry.
	ErrSessionExpired = errors.New("domain: session expired")
	// ErrSessionRevoked is returned when a session was explicitly revoked.
	ErrSessionRevoked = errors.New("domain: session revoked")
	// ErrAssetUnusable is returned when an asset cannot back a timeline clip.
	ErrAssetUnusable = errors.New("domain: asset is not usable for editing")
	// ErrRetentionExpired is returned when an asset outlived its retention.
	ErrRetentionExpired = errors.New("domain: asset retention window elapsed")
)

// ValidationError describes which field failed and why.
type ValidationError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return "domain: field " + e.Field + " " + e.Reason
}

// Unwrap ties the field error to ErrValidation for errors.Is checks.
func (e *ValidationError) Unwrap() error { return ErrValidation }

// NewValidationError builds a field scoped validation failure.
func NewValidationError(field, reason string) *ValidationError {
	return &ValidationError{Field: field, Reason: reason}
}

// TransitionError describes a rejected state machine move.
type TransitionError struct {
	Entity string
	From   string
	To     string
	Reason string
}

// Error implements the error interface.
func (e *TransitionError) Error() string {
	msg := "domain: " + e.Entity + " cannot move from " + e.From + " to " + e.To
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// Unwrap ties the transition error to ErrInvalidTransition.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// NewTransitionError builds a rejected transition error.
func NewTransitionError(entity, from, to, reason string) *TransitionError {
	return &TransitionError{Entity: entity, From: from, To: to, Reason: reason}
}
