package domain

import (
	"strings"
	"time"
)

// AssetStatus enumerates the ingest lifecycle of one media asset.
type AssetStatus string

// Asset statuses. Ingested footage is only usable for editing after its
// checksum has been verified against the transfer manifest.
const (
	AssetIngesting  AssetStatus = "ingesting"
	AssetVerified   AssetStatus = "verified"
	AssetRejected   AssetStatus = "rejected"
	AssetArchived   AssetStatus = "archived"
	AssetQuarantine AssetStatus = "quarantined"
)

// AssetKind enumerates the editorial role of an asset.
type AssetKind string

// Asset kinds.
const (
	AssetKindVideo    AssetKind = "video"
	AssetKindAudio    AssetKind = "audio"
	AssetKindGraphics AssetKind = "graphics"
)

// Valid reports whether the kind is known.
func (k AssetKind) Valid() bool {
	switch k {
	case AssetKindVideo, AssetKindAudio, AssetKindGraphics:
		return true
	default:
		return false
	}
}

// MediaAsset is one ingested source file belonging to a cut project.
type MediaAsset struct {
	ID             string
	ProjectID      string
	Filename       string
	Format         string
	Kind           AssetKind
	Checksum       string
	DeclaredSum    string
	Bytes          int64
	DurationMS     int64
	Status         AssetStatus
	RetentionUntil time.Time
	IngestedAt     time.Time
	VerifiedAt     *time.Time
	RejectReason   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewMediaAsset validates and builds an asset in the ingesting state.
func NewMediaAsset(id, projectID, filename, format string, kind AssetKind, declaredSum string, bytes, durationMS int64, now time.Time, retention time.Duration) (*MediaAsset, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, NewValidationError("project_id", "must not be empty")
	}
	if strings.TrimSpace(filename) == "" {
		return nil, NewValidationError("filename", "must not be empty")
	}
	if strings.TrimSpace(format) == "" {
		return nil, NewValidationError("format", "must not be empty")
	}
	if !kind.Valid() {
		return nil, NewValidationError("kind", "must be video, audio or graphics")
	}
	if len(strings.TrimSpace(declaredSum)) != 64 {
		return nil, NewValidationError("declared_checksum", "must be a 64 character digest")
	}
	if bytes <= 0 {
		return nil, NewValidationError("bytes", "must be positive")
	}
	if durationMS <= 0 {
		return nil, NewValidationError("duration_ms", "must be positive")
	}
	if retention <= 0 {
		return nil, NewValidationError("retention", "must be positive")
	}
	return &MediaAsset{
		ID:             id,
		ProjectID:      projectID,
		Filename:       strings.TrimSpace(filename),
		Format:         strings.ToLower(strings.TrimSpace(format)),
		Kind:           kind,
		DeclaredSum:    strings.ToLower(strings.TrimSpace(declaredSum)),
		Bytes:          bytes,
		DurationMS:     durationMS,
		Status:         AssetIngesting,
		RetentionUntil: now.Add(retention),
		IngestedAt:     now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Verify compares the transferred checksum against the declared manifest value
// and promotes the asset when they match.
func (a *MediaAsset) Verify(observedSum string, now time.Time) error {
	if a.Status != AssetIngesting {
		return NewTransitionError("asset", string(a.Status), string(AssetVerified), "only ingesting assets can be verified")
	}
	observed := strings.ToLower(strings.TrimSpace(observedSum))
	if observed == "" {
		return NewValidationError("observed_checksum", "must not be empty")
	}
	if observed != a.DeclaredSum {
		a.Status = AssetRejected
		a.RejectReason = "checksum mismatch"
		a.UpdatedAt = now
		return NewValidationError("observed_checksum", "does not match the declared manifest digest")
	}
	stamp := now
	a.Checksum = observed
	a.Status = AssetVerified
	a.VerifiedAt = &stamp
	a.UpdatedAt = now
	return nil
}

// Quarantine removes an asset from editorial use without deleting it.
func (a *MediaAsset) Quarantine(reason string, now time.Time) error {
	switch a.Status {
	case AssetVerified, AssetIngesting:
		a.Status = AssetQuarantine
		a.RejectReason = strings.TrimSpace(reason)
		a.UpdatedAt = now
		return nil
	default:
		return NewTransitionError("asset", string(a.Status), string(AssetQuarantine), "only ingesting or verified assets can be quarantined")
	}
}

// Archive moves a verified asset to cold storage.
func (a *MediaAsset) Archive(now time.Time) error {
	if a.Status != AssetVerified {
		return NewTransitionError("asset", string(a.Status), string(AssetArchived), "only verified assets can be archived")
	}
	a.Status = AssetArchived
	a.UpdatedAt = now
	return nil
}

// Usable reports whether the asset may back a timeline clip at the given time.
func (a *MediaAsset) Usable(now time.Time) error {
	if a.Status != AssetVerified {
		return ErrAssetUnusable
	}
	if !now.Before(a.RetentionUntil) {
		return ErrRetentionExpired
	}
	return nil
}

// Referenceable reports the weakest editorial gate: whether a cut may point at
// this footage at all. A reel that was rejected at ingest never existed as far
// as the edit is concerned; every other status (including quarantined or
// archived reels) stays nominally referenceable here. Clip addition uses the
// stricter Usable gate instead, so quarantined or archived footage is blocked
// before it reaches the timeline rather than only at seal time.
func (a *MediaAsset) Referenceable() error {
	if a.Status == AssetRejected {
		return ErrAssetUnusable
	}
	return nil
}

// ExtendRetention pushes the retention deadline forward.
func (a *MediaAsset) ExtendRetention(by time.Duration, now time.Time) error {
	if by <= 0 {
		return NewValidationError("retention_extension", "must be positive")
	}
	if a.Status == AssetRejected {
		return NewTransitionError("asset", string(a.Status), string(a.Status), "rejected assets do not hold retention")
	}
	a.RetentionUntil = a.RetentionUntil.Add(by)
	a.UpdatedAt = now
	return nil
}

// Clone returns an independent copy so repository callers cannot mutate cached
// state through shared pointers.
func (a *MediaAsset) Clone() *MediaAsset {
	if a == nil {
		return nil
	}
	copied := *a
	if a.VerifiedAt != nil {
		stamp := *a.VerifiedAt
		copied.VerifiedAt = &stamp
	}
	return &copied
}

// AssetFilter narrows an asset listing.
type AssetFilter struct {
	ProjectID string
	Status    AssetStatus
	Kind      AssetKind
	Search    string
}
