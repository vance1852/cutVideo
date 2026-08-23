// Package editing owns cut projects and their timeline versions: drafting,
// clip assembly, sealing and the project level locks the render farm relies on.
package editing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/audit"
	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/repository"
)

// Service implements the editorial use cases.
type Service struct {
	store    repository.Store
	recorder *audit.Recorder
	gen      ids.Generator
	clk      clock.Clock
	logger   *slog.Logger
}

// New builds the editing service.
func New(store repository.Store, recorder *audit.Recorder, gen ids.Generator, clk clock.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Service{store: store, recorder: recorder, gen: gen, clk: clk, logger: logger}
}

// CreateProjectInput describes a new cut project.
type CreateProjectInput struct {
	Code       string
	Title      string
	FrameRate  int
	Resolution string
	Deadline   time.Time
}

// CreateProject registers a project owned by the caller.
func (s *Service) CreateProject(ctx context.Context, actor domain.Principal, input CreateProjectInput) (*domain.Project, error) {
	if err := actor.RequireEdit(); err != nil {
		return nil, apierr.Wrap(apierr.CodeForbidden, "your role may not create projects", err)
	}
	now := s.clk.Now()
	project, err := domain.NewProject(s.gen.NewID("prj"), input.Code, input.Title, actor.UserID,
		input.FrameRate, input.Resolution, input.Deadline, now)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "the project details are not acceptable", err)
	}
	err = s.store.InTx(ctx, func(txCtx context.Context) error {
		if err := s.store.Projects().Create(txCtx, project); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, domain.ActionProjectCreate, audit.ObjectProject, project.ID,
			"code="+project.Code)
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, apierr.Wrap(apierr.CodeConflict, "that project code is already taken", err)
		}
		return nil, apierr.Wrap(apierr.CodeInternal, "could not create the project", err)
	}
	return project, nil
}

// GetProject returns a project the caller may read.
func (s *Service) GetProject(ctx context.Context, actor domain.Principal, projectID string) (*domain.Project, error) {
	project, err := s.store.Projects().GetByID(ctx, projectID)
	if err != nil {
		return nil, translate(err, "could not read the project")
	}
	if err := s.ensureReadAccess(actor, project); err != nil {
		return nil, err
	}
	return project, nil
}

// ListProjects returns a filtered page of projects. Editors only see their own.
func (s *Service) ListProjects(ctx context.Context, actor domain.Principal, filter domain.ProjectFilter, page domain.Page) (domain.PageResult[*domain.Project], error) {
	var empty domain.PageResult[*domain.Project]
	if actor.IsZero() {
		return empty, apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	if actor.Role == domain.RoleEditor {
		filter.OwnerID = actor.UserID
	}
	result, err := s.store.Projects().List(ctx, filter, page)
	if err != nil {
		return empty, apierr.Wrap(apierr.CodeInternal, "could not list projects", err)
	}
	return result, nil
}

// LockProject freezes editorial changes while a master render runs.
func (s *Service) LockProject(ctx context.Context, actor domain.Principal, projectID string) (*domain.Project, error) {
	now := s.clk.Now()
	var locked *domain.Project
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		project, err := s.store.Projects().GetByID(txCtx, projectID)
		if err != nil {
			return err
		}
		if err := project.EnsureWriteAccess(actor); err != nil {
			return err
		}
		if err := project.Lock(now); err != nil {
			return err
		}
		if err := s.store.Projects().Update(txCtx, project); err != nil {
			return err
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionProjectLock, audit.ObjectProject, project.ID,
			fmt.Sprintf("sealed_version=%d", project.SealedVersion)); err != nil {
			return err
		}
		locked = project
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not lock the project")
	}
	return locked, nil
}

// UnlockProject returns a locked project to editing.
func (s *Service) UnlockProject(ctx context.Context, actor domain.Principal, projectID string) (*domain.Project, error) {
	now := s.clk.Now()
	var unlocked *domain.Project
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		project, err := s.store.Projects().GetByID(txCtx, projectID)
		if err != nil {
			return err
		}
		if err := project.EnsureWriteAccess(actor); err != nil {
			return err
		}
		if err := project.Unlock(now); err != nil {
			return err
		}
		if err := s.store.Projects().Update(txCtx, project); err != nil {
			return err
		}
		unlocked = project
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not unlock the project")
	}
	return unlocked, nil
}

// CreateDraft opens the next timeline version for a project. Only one draft may
// exist at a time so two editors cannot silently fork the edit decision list.
func (s *Service) CreateDraft(ctx context.Context, actor domain.Principal, projectID, notes string) (*domain.TimelineVersion, error) {
	now := s.clk.Now()
	var draft *domain.TimelineVersion
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		project, err := s.store.Projects().GetByID(txCtx, projectID)
		if err != nil {
			return err
		}
		if err := project.EnsureWriteAccess(actor); err != nil {
			return err
		}
		if err := project.EnsureEditable(); err != nil {
			return err
		}
		existing, err := s.store.Timelines().List(txCtx, domain.TimelineFilter{
			ProjectID: project.ID,
			Status:    domain.TimelineDraft,
		}, domain.Page{Number: 1, Size: 1, Sort: domain.SortDescending})
		if err != nil {
			return err
		}
		if existing.Total > 0 {
			return fmt.Errorf("editing: project %s already has an open draft: %w", project.ID, domain.ErrConflict)
		}
		version, err := domain.NewTimelineVersion(s.gen.NewID("tml"), project.ID, project.NextVersion(), actor.UserID, notes, now)
		if err != nil {
			return err
		}
		if err := s.store.Timelines().CreateVersion(txCtx, version); err != nil {
			return err
		}
		if err := project.RecordDraft(version.Version, now); err != nil {
			return err
		}
		if err := s.store.Projects().Update(txCtx, project); err != nil {
			return err
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionTimelineDraft, audit.ObjectTimeline, version.ID,
			fmt.Sprintf("version=%d", version.Version)); err != nil {
			return err
		}
		draft = version
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not open a new timeline version")
	}
	return draft, nil
}

// AddClipInput describes one clip appended to a draft timeline.
type AddClipInput struct {
	TimelineID   string
	AssetID      string
	OrderIndex   int
	SourceInMS   int64
	SourceOutMS  int64
	Track        domain.Track
	Transition   string
	SpeedPercent int
}

// AddClip inserts a clip into a draft timeline. Timeline counters and the clip
// row are written in the same transaction so the stored duration always matches
// the stored clips.
func (s *Service) AddClip(ctx context.Context, actor domain.Principal, input AddClipInput) (*domain.TimelineVersion, error) {
	now := s.clk.Now()
	if input.SpeedPercent == 0 {
		input.SpeedPercent = 100
	}
	var updated *domain.TimelineVersion
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		version, err := s.store.Timelines().GetVersion(txCtx, input.TimelineID)
		if err != nil {
			return err
		}
		project, err := s.store.Projects().GetByID(txCtx, version.ProjectID)
		if err != nil {
			return err
		}
		if err := project.EnsureWriteAccess(actor); err != nil {
			return err
		}
		if err := project.EnsureEditable(); err != nil {
			return err
		}
		if err := version.EnsureEditable(); err != nil {
			return err
		}
		asset, err := s.store.Assets().GetByID(txCtx, input.AssetID)
		if err != nil {
			return err
		}
		if asset.ProjectID != project.ID {
			return fmt.Errorf("editing: asset %s belongs to another project: %w", asset.ID, domain.ErrValidation)
		}
		if err := asset.Usable(now); err != nil {
			return err
		}
		clip, err := domain.NewClip(s.gen.NewID("clp"), version.ID, asset.ID, input.OrderIndex,
			input.SourceInMS, input.SourceOutMS, input.Track, input.Transition, input.SpeedPercent, now)
		if err != nil {
			return err
		}
		if err := clip.FitsAsset(asset); err != nil {
			return err
		}
		expected := version.RowVersion
		if err := version.AddClip(clip, now); err != nil {
			return err
		}
		if err := s.store.Timelines().AddClip(txCtx, clip); err != nil {
			return err
		}
		version.RowVersion = expected + 1
		if err := s.store.Timelines().UpdateVersion(txCtx, version, expected); err != nil {
			return err
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionTimelineClipAdd, audit.ObjectTimeline, version.ID,
			"clip="+clip.ID); err != nil {
			return err
		}
		updated = version
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not add the clip")
	}
	return updated, nil
}

// RemoveClip deletes a clip from a draft timeline and refreshes the counters.
func (s *Service) RemoveClip(ctx context.Context, actor domain.Principal, timelineID, clipID string) (*domain.TimelineVersion, error) {
	now := s.clk.Now()
	var updated *domain.TimelineVersion
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		version, err := s.store.Timelines().GetVersion(txCtx, timelineID)
		if err != nil {
			return err
		}
		project, err := s.store.Projects().GetByID(txCtx, version.ProjectID)
		if err != nil {
			return err
		}
		if err := project.EnsureWriteAccess(actor); err != nil {
			return err
		}
		if err := version.EnsureEditable(); err != nil {
			return err
		}
		expected := version.RowVersion
		if err := version.RemoveClip(clipID, now); err != nil {
			return err
		}
		if err := s.store.Timelines().RemoveClip(txCtx, timelineID, clipID); err != nil {
			return err
		}
		version.RowVersion = expected + 1
		if err := s.store.Timelines().UpdateVersion(txCtx, version, expected); err != nil {
			return err
		}
		updated = version
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not remove the clip")
	}
	return updated, nil
}

// Seal freezes a draft timeline. The project sealed pointer and the previously
// sealed version are updated in the same transaction, so a failure anywhere
// leaves the project exactly as it was.
func (s *Service) Seal(ctx context.Context, actor domain.Principal, timelineID string) (*domain.TimelineVersion, error) {
	now := s.clk.Now()
	var sealed *domain.TimelineVersion
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		version, err := s.store.Timelines().GetVersion(txCtx, timelineID)
		if err != nil {
			return err
		}
		project, err := s.store.Projects().GetByID(txCtx, version.ProjectID)
		if err != nil {
			return err
		}
		if err := project.EnsureWriteAccess(actor); err != nil {
			return err
		}
		if err := project.EnsureEditable(); err != nil {
			return err
		}
		clips := version.Clips()
		assetIDs := make([]string, 0, len(clips))
		for _, clip := range clips {
			assetIDs = append(assetIDs, clip.AssetID)
		}
		assets, err := s.store.Assets().ListByIDs(txCtx, project.ID, assetIDs)
		if err != nil {
			return err
		}
		index := make(map[string]*domain.MediaAsset, len(assets))
		for _, asset := range assets {
			index[asset.ID] = asset
		}
		expected := version.RowVersion
		if err := version.Seal(index, now); err != nil {
			return err
		}
		if err := s.store.Timelines().UpdateVersion(txCtx, version, expected); err != nil {
			return err
		}
		if project.SealedVersion > 0 {
			// Retire whatever the project currently advertises as its sealed cut.
			previous, err := s.store.Timelines().CurrentSealed(txCtx, project.ID)
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if err == nil && previous.Status == domain.TimelineSealed {
				previousExpected := previous.RowVersion
				if err := previous.Supersede(now); err != nil {
					return err
				}
				if err := s.store.Timelines().UpdateVersion(txCtx, previous, previousExpected); err != nil {
					return err
				}
			}
		}
		if err := project.RecordSeal(version.Version, now); err != nil {
			return err
		}
		if err := s.store.Projects().Update(txCtx, project); err != nil {
			return err
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionTimelineSeal, audit.ObjectTimeline, version.ID,
			fmt.Sprintf("version=%d duration_ms=%d", version.Version, version.TotalDurationMS)); err != nil {
			return err
		}
		sealed = version
		return nil
	})
	if err != nil {
		return nil, translate(err, "could not seal the timeline version")
	}
	logging.FromContext(ctx, s.logger).Info("timeline sealed", "timeline_id", sealed.ID, "version", sealed.Version)
	return sealed, nil
}

// GetTimeline returns one timeline version with its clips.
func (s *Service) GetTimeline(ctx context.Context, actor domain.Principal, timelineID string) (*domain.TimelineVersion, error) {
	version, err := s.store.Timelines().GetVersion(ctx, timelineID)
	if err != nil {
		return nil, translate(err, "could not read the timeline version")
	}
	project, err := s.store.Projects().GetByID(ctx, version.ProjectID)
	if err != nil {
		return nil, translate(err, "could not read the project")
	}
	if err := s.ensureReadAccess(actor, project); err != nil {
		return nil, err
	}
	return version, nil
}

// ListTimelines returns a page of timeline versions for a project.
func (s *Service) ListTimelines(ctx context.Context, actor domain.Principal, filter domain.TimelineFilter, page domain.Page) (domain.PageResult[*domain.TimelineVersion], error) {
	var empty domain.PageResult[*domain.TimelineVersion]
	if strings.TrimSpace(filter.ProjectID) == "" {
		return empty, apierr.Wrap(apierr.CodeInvalidRequest, "a project is required",
			domain.NewValidationError("project_id", "must not be empty"))
	}
	project, err := s.store.Projects().GetByID(ctx, filter.ProjectID)
	if err != nil {
		return empty, translate(err, "could not read the project")
	}
	if err := s.ensureReadAccess(actor, project); err != nil {
		return empty, err
	}
	result, err := s.store.Timelines().List(ctx, filter, page)
	if err != nil {
		return empty, apierr.Wrap(apierr.CodeInternal, "could not list timeline versions", err)
	}
	return result, nil
}

func (s *Service) ensureReadAccess(actor domain.Principal, project *domain.Project) error {
	if actor.IsZero() {
		return apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	if project.OwnedBy(actor) || actor.Role != domain.RoleEditor {
		return nil
	}
	return apierr.Wrap(apierr.CodeForbidden, "you may not read this project", domain.ErrPermissionDenied)
}

func translate(err error, message string) error {
	var public *apierr.Error
	if errors.As(err, &public) {
		return public
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return apierr.Wrap(apierr.CodeNotFound, "that record does not exist", err)
	case errors.Is(err, domain.ErrVersionConflict):
		return apierr.Wrap(apierr.CodeConflict, "someone else changed this timeline version, reload and retry", err)
	case errors.Is(err, domain.ErrRetentionExpired):
		return apierr.Wrap(apierr.CodePreconditionFail, "referenced footage has passed its retention window", err)
	case errors.Is(err, domain.ErrAssetUnusable):
		return apierr.Wrap(apierr.CodePreconditionFail, "referenced footage is not verified for editing", err)
	case errors.Is(err, domain.ErrValidation):
		return apierr.Wrap(apierr.CodeInvalidRequest, message, err)
	case errors.Is(err, domain.ErrInvalidTransition):
		return apierr.Wrap(apierr.CodePreconditionFail, message, err)
	case errors.Is(err, domain.ErrConflict):
		return apierr.Wrap(apierr.CodeConflict, message, err)
	case errors.Is(err, domain.ErrPermissionDenied):
		return apierr.Wrap(apierr.CodeForbidden, "you may not perform this action", err)
	default:
		return apierr.Wrap(apierr.CodeInternal, message, err)
	}
}

// Translate exposes the shared error mapping to sibling services.
func Translate(err error, message string) error { return translate(err, message) }
