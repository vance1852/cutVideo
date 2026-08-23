package domain

import (
	"strings"
	"time"
)

// Session is a server side, revocable credential for one signed in operator.
// Only the token hash is persisted; the raw bearer token exists solely in the
// sign in response.
type Session struct {
	ID         string
	UserID     string
	TokenHash  string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	RevokedAt  *time.Time
	UserAgent  string
}

// NewSession validates and builds a session.
func NewSession(id, userID, tokenHash, userAgent string, issuedAt time.Time, ttl time.Duration) (*Session, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("session_id", "must not be empty")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, NewValidationError("user_id", "must not be empty")
	}
	if strings.TrimSpace(tokenHash) == "" {
		return nil, NewValidationError("token", "must not be empty")
	}
	if ttl <= 0 {
		return nil, NewValidationError("session_ttl", "must be positive")
	}
	return &Session{
		ID:         id,
		UserID:     userID,
		TokenHash:  tokenHash,
		IssuedAt:   issuedAt,
		ExpiresAt:  issuedAt.Add(ttl),
		LastSeenAt: issuedAt,
		UserAgent:  strings.TrimSpace(userAgent),
	}, nil
}

// Revoked reports whether the session was explicitly revoked.
func (s *Session) Revoked() bool { return s.RevokedAt != nil }

// Expired reports whether the session is past its expiry instant.
func (s *Session) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

// EnsureUsable reports why a session may not authenticate a request.
func (s *Session) EnsureUsable(now time.Time) error {
	if s.Revoked() {
		return ErrSessionRevoked
	}
	if s.Expired(now) {
		return ErrSessionExpired
	}
	return nil
}

// Revoke marks the session unusable.
func (s *Session) Revoke(now time.Time) error {
	if s.Revoked() {
		return NewTransitionError("session", "revoked", "revoked", "already revoked")
	}
	stamp := now
	s.RevokedAt = &stamp
	return nil
}

// Touch records activity on the session. A revocation is permanent: live traffic
// never reopens a session that an operator or a supervisor already closed, so
// Touch only refreshes the last seen timestamp.
func (s *Session) Touch(now time.Time) {
	s.LastSeenAt = now
}

// RemainingTTL reports how long the session stays valid.
func (s *Session) RemainingTTL(now time.Time) time.Duration {
	if s.Expired(now) {
		return 0
	}
	return s.ExpiresAt.Sub(now)
}

// Principal is the authenticated identity attached to a request context.
type Principal struct {
	UserID    string
	SessionID string
	Role      Role
	Email     string
}

// IsZero reports whether no principal is present.
func (p Principal) IsZero() bool { return p.UserID == "" }

// RequireEdit enforces editorial write authority.
func (p Principal) RequireEdit() error {
	if !p.Role.CanEdit() {
		return ErrPermissionDenied
	}
	return nil
}

// RequireFarmArbitration enforces render farm authority over other users' work.
func (p Principal) RequireFarmArbitration() error {
	if !p.Role.CanArbitrateFarm() {
		return ErrPermissionDenied
	}
	return nil
}

// RequireDeliveryManagement enforces delivery target authority.
func (p Principal) RequireDeliveryManagement() error {
	if !p.Role.CanManageDelivery() {
		return ErrPermissionDenied
	}
	return nil
}
