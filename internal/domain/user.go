package domain

import (
	"net/mail"
	"strings"
	"time"
)

// Role enumerates the editorial roles with distinct business authority.
type Role string

// Roles. Editors own their own cut projects; supervisors additionally arbitrate
// the shared render farm and manage delivery targets; auditors are read only.
const (
	RoleEditor     Role = "editor"
	RoleSupervisor Role = "supervisor"
	RoleAuditor    Role = "auditor"
)

// Valid reports whether the role is known.
func (r Role) Valid() bool {
	switch r {
	case RoleEditor, RoleSupervisor, RoleAuditor:
		return true
	default:
		return false
	}
}

// CanEdit reports whether the role may mutate editorial content.
func (r Role) CanEdit() bool { return r == RoleEditor || r == RoleSupervisor }

// CanArbitrateFarm reports whether the role may cancel or reprioritize render
// work owned by another user.
func (r Role) CanArbitrateFarm() bool { return r == RoleSupervisor }

// CanManageDelivery reports whether the role may create or disable delivery
// targets.
func (r Role) CanManageDelivery() bool { return r == RoleSupervisor }

// UserStatus enumerates account states.
type UserStatus string

// User statuses.
const (
	UserActive    UserStatus = "active"
	UserSuspended UserStatus = "suspended"
)

// User is an authenticated operator of the platform.
type User struct {
	ID           string
	Email        string
	DisplayName  string
	Role         Role
	Status       UserStatus
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewUser validates and builds an active user.
func NewUser(id, email, displayName string, role Role, passwordHash string, now time.Time) (*User, error) {
	normalizedEmail, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(displayName) == "" {
		return nil, NewValidationError("display_name", "must not be empty")
	}
	if !role.Valid() {
		return nil, NewValidationError("role", "must be editor, supervisor or auditor")
	}
	if strings.TrimSpace(passwordHash) == "" {
		return nil, NewValidationError("password", "must not be empty")
	}
	return &User{
		ID:           id,
		Email:        normalizedEmail,
		DisplayName:  strings.TrimSpace(displayName),
		Role:         role,
		Status:       UserActive,
		PasswordHash: passwordHash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Active reports whether the account may authenticate.
func (u *User) Active() bool { return u.Status == UserActive }

// Suspend disables the account.
func (u *User) Suspend(now time.Time) error {
	if u.Status == UserSuspended {
		return NewTransitionError("user", string(u.Status), string(UserSuspended), "already suspended")
	}
	u.Status = UserSuspended
	u.UpdatedAt = now
	return nil
}

// Reinstate re-enables a suspended account.
func (u *User) Reinstate(now time.Time) error {
	if u.Status == UserActive {
		return NewTransitionError("user", string(u.Status), string(UserActive), "already active")
	}
	u.Status = UserActive
	u.UpdatedAt = now
	return nil
}

// NormalizeEmail lower cases and validates an address.
func NormalizeEmail(email string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(email))
	if trimmed == "" {
		return "", NewValidationError("email", "must not be empty")
	}
	if _, err := mail.ParseAddress(trimmed); err != nil {
		return "", NewValidationError("email", "must be a valid address")
	}
	return trimmed, nil
}
