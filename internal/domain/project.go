package domain

import (
	"regexp"
	"strings"
	"time"
)

// ProjectStatus enumerates the editorial lifecycle of a cut project.
type ProjectStatus string

// Project statuses.
const (
	ProjectActive   ProjectStatus = "active"
	ProjectLocked   ProjectStatus = "locked"
	ProjectDelivered ProjectStatus = "delivered"
	ProjectArchived ProjectStatus = "archived"
)

var projectCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,11}$`)

// Project groups media, timeline versions and render work for one deliverable.
type Project struct {
	ID             string
	Code           string
	Title          string
	OwnerID        string
	Status         ProjectStatus
	FrameRate      int
	Resolution     string
	CurrentVersion int
	SealedVersion  int
	DeadlineAt     time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewProject validates and builds an active project.
func NewProject(id, code, title, ownerID string, frameRate int, resolution string, deadline, now time.Time) (*Project, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	normalizedCode := strings.ToUpper(strings.TrimSpace(code))
	if !projectCodePattern.MatchString(normalizedCode) {
		return nil, NewValidationError("code", "must be 3 to 12 upper case alphanumeric characters starting with a letter")
	}
	if strings.TrimSpace(title) == "" {
		return nil, NewValidationError("title", "must not be empty")
	}
	if strings.TrimSpace(ownerID) == "" {
		return nil, NewValidationError("owner_id", "must not be empty")
	}
	if frameRate != 24 && frameRate != 25 && frameRate != 30 && frameRate != 50 && frameRate != 60 {
		return nil, NewValidationError("frame_rate", "must be one of 24, 25, 30, 50 or 60")
	}
	if strings.TrimSpace(resolution) == "" {
		return nil, NewValidationError("resolution", "must not be empty")
	}
	if !deadline.After(now) {
		return nil, NewValidationError("deadline_at", "must be in the future")
	}
	return &Project{
		ID:             id,
		Code:           normalizedCode,
		Title:          strings.TrimSpace(title),
		OwnerID:        strings.TrimSpace(ownerID),
		Status:         ProjectActive,
		FrameRate:      frameRate,
		Resolution:     strings.TrimSpace(resolution),
		CurrentVersion: 0,
		SealedVersion:  0,
		DeadlineAt:     deadline,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Editable reports whether editorial changes are still accepted.
func (p *Project) Editable() bool { return p.Status == ProjectActive }

// EnsureEditable returns a transition error when the project is frozen.
func (p *Project) EnsureEditable() error {
	if p.Editable() {
		return nil
	}
	return NewTransitionError("project", string(p.Status), string(ProjectActive), "project no longer accepts editorial changes")
}

// OwnedBy reports whether the principal owns the project.
func (p *Project) OwnedBy(principal Principal) bool { return p.OwnerID == principal.UserID }

// EnsureWriteAccess allows the owner, and supervisors on any project.
func (p *Project) EnsureWriteAccess(principal Principal) error {
	if err := principal.RequireEdit(); err != nil {
		return err
	}
	if p.OwnedBy(principal) || principal.Role.CanArbitrateFarm() {
		return nil
	}
	return ErrPermissionDenied
}

// Lock freezes editorial changes while a master render is in flight.
func (p *Project) Lock(now time.Time) error {
	if p.Status != ProjectActive {
		return NewTransitionError("project", string(p.Status), string(ProjectLocked), "only active projects can be locked")
	}
	if p.SealedVersion == 0 {
		return NewTransitionError("project", string(p.Status), string(ProjectLocked), "a sealed timeline version is required")
	}
	p.Status = ProjectLocked
	p.UpdatedAt = now
	return nil
}

// Unlock returns a locked project to editing.
func (p *Project) Unlock(now time.Time) error {
	if p.Status != ProjectLocked {
		return NewTransitionError("project", string(p.Status), string(ProjectActive), "only locked projects can be unlocked")
	}
	p.Status = ProjectActive
	p.UpdatedAt = now
	return nil
}

// MarkDelivered records that at least one render reached every active target.
func (p *Project) MarkDelivered(now time.Time) error {
	switch p.Status {
	case ProjectActive, ProjectLocked:
		p.Status = ProjectDelivered
		p.UpdatedAt = now
		return nil
	default:
		return NewTransitionError("project", string(p.Status), string(ProjectDelivered), "only active or locked projects can be delivered")
	}
}

// Archive closes the project for good.
func (p *Project) Archive(now time.Time) error {
	if p.Status == ProjectArchived {
		return NewTransitionError("project", string(p.Status), string(ProjectArchived), "already archived")
	}
	p.Status = ProjectArchived
	p.UpdatedAt = now
	return nil
}

// NextVersion returns the version number that a new draft timeline receives.
func (p *Project) NextVersion() int { return p.CurrentVersion + 1 }

// RecordDraft advances the highest allocated version number.
func (p *Project) RecordDraft(version int, now time.Time) error {
	if version != p.NextVersion() {
		return NewValidationError("version", "must be the next sequential timeline version")
	}
	p.CurrentVersion = version
	p.UpdatedAt = now
	return nil
}

// RecordSeal advances the highest sealed version number.
func (p *Project) RecordSeal(version int, now time.Time) error {
	if version <= p.SealedVersion {
		return NewValidationError("version", "must be newer than the currently sealed version")
	}
	if version > p.CurrentVersion {
		return NewValidationError("version", "cannot seal a version that was never drafted")
	}
	p.SealedVersion = version
	p.UpdatedAt = now
	return nil
}

// PastDeadline reports whether the editorial deadline elapsed.
func (p *Project) PastDeadline(now time.Time) bool { return !now.Before(p.DeadlineAt) }

// Clone returns an independent copy of the project.
func (p *Project) Clone() *Project {
	if p == nil {
		return nil
	}
	copied := *p
	return &copied
}

// ProjectFilter narrows a project listing.
type ProjectFilter struct {
	OwnerID string
	Status  ProjectStatus
	Search  string
}
