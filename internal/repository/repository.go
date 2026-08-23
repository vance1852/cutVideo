// Package repository declares the persistence contracts used by the service
// layer. Implementations live under internal/storage and must not leak SQL
// details through these interfaces.
package repository

import (
	"context"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

// TxFunc is the body of a transactional unit of work. Every repository call made
// with the supplied context participates in the same transaction.
type TxFunc func(ctx context.Context) error

// TxManager runs a unit of work inside one database transaction.
type TxManager interface {
	InTx(ctx context.Context, fn TxFunc) error
}

// UserRepository persists operator accounts.
type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	Update(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id string) (*domain.User, error)
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	List(ctx context.Context, page domain.Page) (domain.PageResult[*domain.User], error)
}

// SessionRepository persists revocable sign in sessions.
type SessionRepository interface {
	Create(ctx context.Context, session *domain.Session) error
	Update(ctx context.Context, session *domain.Session) error
	GetByID(ctx context.Context, id string) (*domain.Session, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error)
	RevokeAllForUser(ctx context.Context, userID string, at time.Time) (int, error)
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// ProjectRepository persists cut projects.
type ProjectRepository interface {
	Create(ctx context.Context, project *domain.Project) error
	Update(ctx context.Context, project *domain.Project) error
	GetByID(ctx context.Context, id string) (*domain.Project, error)
	GetByCode(ctx context.Context, code string) (*domain.Project, error)
	List(ctx context.Context, filter domain.ProjectFilter, page domain.Page) (domain.PageResult[*domain.Project], error)
}

// AssetRepository persists ingested media assets.
type AssetRepository interface {
	Create(ctx context.Context, asset *domain.MediaAsset) error
	Update(ctx context.Context, asset *domain.MediaAsset) error
	GetByID(ctx context.Context, id string) (*domain.MediaAsset, error)
	GetByDeclaredChecksum(ctx context.Context, projectID, checksum string) (*domain.MediaAsset, error)
	ListByIDs(ctx context.Context, projectID string, ids []string) ([]*domain.MediaAsset, error)
	List(ctx context.Context, filter domain.AssetFilter, page domain.Page) (domain.PageResult[*domain.MediaAsset], error)
}

// TimelineRepository persists timeline versions and their clips.
type TimelineRepository interface {
	CreateVersion(ctx context.Context, version *domain.TimelineVersion) error
	UpdateVersion(ctx context.Context, version *domain.TimelineVersion, expectedRowVersion int) error
	GetVersion(ctx context.Context, id string) (*domain.TimelineVersion, error)
	GetVersionByNumber(ctx context.Context, projectID string, version int) (*domain.TimelineVersion, error)
	LatestSealed(ctx context.Context, projectID string) (*domain.TimelineVersion, error)
	// CurrentSealed returns the version a project currently advertises as its
	// sealed cut, so a caller does not have to track version numbers itself.
	CurrentSealed(ctx context.Context, projectID string) (*domain.TimelineVersion, error)
	List(ctx context.Context, filter domain.TimelineFilter, page domain.Page) (domain.PageResult[*domain.TimelineVersion], error)
	AddClip(ctx context.Context, clip *domain.Clip) error
	RemoveClip(ctx context.Context, timelineID, clipID string) error
	ListClips(ctx context.Context, timelineID string) ([]*domain.Clip, error)
}

// RenderRepository persists render jobs and exposes the queue queries used by
// the background worker.
type RenderRepository interface {
	Create(ctx context.Context, job *domain.RenderJob) error
	Update(ctx context.Context, job *domain.RenderJob, expectedRowVersion int) error
	GetByID(ctx context.Context, id string) (*domain.RenderJob, error)
	FindByIdempotencyKey(ctx context.Context, projectID, key string) (*domain.RenderJob, error)
	CountActiveForTimeline(ctx context.Context, timelineID string) (int, error)
	NextEligible(ctx context.Context, now time.Time, limit int) ([]*domain.RenderJob, error)
	ListExpiredLeases(ctx context.Context, now time.Time, limit int) ([]*domain.RenderJob, error)
	List(ctx context.Context, filter domain.RenderFilter, page domain.Page) (domain.PageResult[*domain.RenderJob], error)
}

// SlotRepository persists render farm seats and enforces exclusive ownership
// with conditional updates.
type SlotRepository interface {
	Create(ctx context.Context, slot *domain.RenderSlot) error
	GetByID(ctx context.Context, id string) (*domain.RenderSlot, error)
	ReserveIdle(ctx context.Context, pool, jobID string, now time.Time) (*domain.RenderSlot, error)
	Release(ctx context.Context, slotID, jobID string, now time.Time) error
	ForceRelease(ctx context.Context, slotID string, now time.Time) error
	List(ctx context.Context, pool string) ([]*domain.RenderSlot, error)
	Capacity(ctx context.Context, pool string) (domain.FarmCapacity, error)
}

// DeliveryRepository persists distribution targets and dispatch records.
type DeliveryRepository interface {
	CreateTarget(ctx context.Context, target *domain.DeliveryTarget) error
	UpdateTarget(ctx context.Context, target *domain.DeliveryTarget) error
	GetTarget(ctx context.Context, id string) (*domain.DeliveryTarget, error)
	GetTargetByName(ctx context.Context, projectID, name string) (*domain.DeliveryTarget, error)
	ListTargets(ctx context.Context, projectID string, onlyEnabled bool) ([]*domain.DeliveryTarget, error)
	CreateRecord(ctx context.Context, record *domain.DeliveryRecord) error
	UpdateRecord(ctx context.Context, record *domain.DeliveryRecord) error
	GetRecord(ctx context.Context, jobID, targetID string) (*domain.DeliveryRecord, error)
	ListRecordsForJob(ctx context.Context, jobID string) ([]*domain.DeliveryRecord, error)
}

// AuditRepository appends and queries durable audit events.
type AuditRepository interface {
	Append(ctx context.Context, event *domain.AuditEvent) error
	List(ctx context.Context, filter domain.AuditFilter, page domain.Page) (domain.PageResult[*domain.AuditEvent], error)
}

// IdempotencyRepository stores replayable mutating responses.
type IdempotencyRepository interface {
	Get(ctx context.Context, scope, method, path, actorID, key string) (*domain.IdempotencyRecord, error)
	Put(ctx context.Context, record *domain.IdempotencyRecord) error
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// Store aggregates every repository behind one transaction manager.
type Store interface {
	TxManager
	Users() UserRepository
	Sessions() SessionRepository
	Projects() ProjectRepository
	Assets() AssetRepository
	Timelines() TimelineRepository
	Renders() RenderRepository
	Slots() SlotRepository
	Deliveries() DeliveryRepository
	Audits() AuditRepository
	Idempotency() IdempotencyRepository
	Ping(ctx context.Context) error
}
