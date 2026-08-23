// Package media owns the ingest lifecycle of source footage: registration,
// checksum verification against the transfer manifest, quarantine and retention.
package media

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
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/repository"
)

// Service implements media ingest use cases.
type Service struct {
	store    repository.Store
	recorder *audit.Recorder
	gen      ids.Generator
	clk      clock.Clock
	cfg      config.Config
	logger   *slog.Logger
}

// New builds the media service.
func New(store repository.Store, recorder *audit.Recorder, gen ids.Generator, clk clock.Clock, cfg config.Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Service{store: store, recorder: recorder, gen: gen, clk: clk, cfg: cfg, logger: logger}
}

// IngestInput registers one incoming source file.
type IngestInput struct {
	ProjectID  string
	Filename   string
	Format     string
	Kind       domain.AssetKind
	DeclaredMD string
	Bytes      int64
	DurationMS int64
}

// Ingest registers footage in the ingesting state.
func (s *Service) Ingest(ctx context.Context, actor domain.Principal, input IngestInput) (*domain.MediaAsset, error) {
	project, err := s.loadWritableProject(ctx, actor, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if !s.cfg.AllowsFormat(input.Format) {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest,
			fmt.Sprintf("container format %q is not accepted for ingest", input.Format),
			domain.NewValidationError("format", "is not an accepted container"))
	}
	if input.Bytes > s.cfg.Media.MaxAssetBytes {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "this file is larger than the ingest limit",
			domain.NewValidationError("bytes", "exceeds the ingest limit"))
	}
	now := s.clk.Now()
	asset, err := domain.NewMediaAsset(
		s.gen.NewID("ast"), project.ID, input.Filename, input.Format, input.Kind,
		input.DeclaredMD, input.Bytes, input.DurationMS, now, s.cfg.Media.Retention,
	)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "the ingest request is not acceptable", err)
	}
	err = s.store.InTx(ctx, func(txCtx context.Context) error {
		if err := s.store.Assets().Create(txCtx, asset); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, domain.ActionAssetIngest, audit.ObjectAsset, asset.ID,
			"filename="+asset.Filename)
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, apierr.Wrap(apierr.CodeConflict, "this footage was already registered for the project", err)
		}
		return nil, apierr.Wrap(apierr.CodeInternal, "could not register the footage", err)
	}
	logging.FromContext(ctx, s.logger).Info("asset ingested", "asset_id", asset.ID, "project_id", project.ID)
	return asset, nil
}

// Verify compares the transferred bytes against the declared manifest digest.
func (s *Service) Verify(ctx context.Context, actor domain.Principal, assetID, observedSum string) (*domain.MediaAsset, error) {
	now := s.clk.Now()
	var (
		verified  *domain.MediaAsset
		verifyErr error
	)
	// A checksum mismatch is a business outcome, not an infrastructure failure:
	// the rejected state and its audit event must be committed, and the rejection
	// is reported to the caller afterwards.
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		asset, err := s.store.Assets().GetByID(txCtx, assetID)
		if err != nil {
			return err
		}
		if _, err := s.loadWritableProject(txCtx, actor, asset.ProjectID); err != nil {
			return err
		}
		verifyErr = asset.Verify(observedSum, now)
		if err := s.store.Assets().Update(txCtx, asset); err != nil {
			return err
		}
		if verifyErr != nil {
			return s.recorder.Rejected(txCtx, actor, domain.ActionAssetVerify, audit.ObjectAsset, asset.ID,
				"checksum mismatch")
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionAssetVerify, audit.ObjectAsset, asset.ID,
			"checksum verified"); err != nil {
			return err
		}
		verified = asset
		return nil
	})
	if err != nil {
		return nil, s.translate(err, "could not verify the footage")
	}
	if verifyErr != nil {
		return nil, s.translate(verifyErr, "the transferred footage does not match the declared manifest digest")
	}
	return verified, nil
}

// VerifyBatchItem is one entry of a batch verification request.
type VerifyBatchItem struct {
	AssetID     string
	ObservedSum string
}

// VerifyOutcome reports the result for one batch entry.
type VerifyOutcome struct {
	AssetID string
	Status  domain.AssetStatus
	Message string
}

// VerifyBatch verifies several assets and reports per item results. One rejected
// entry does not roll back the entries that verified cleanly.
func (s *Service) VerifyBatch(ctx context.Context, actor domain.Principal, items []VerifyBatchItem) ([]VerifyOutcome, error) {
	if len(items) == 0 {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "at least one entry is required",
			domain.NewValidationError("items", "must not be empty"))
	}
	outcomes := make([]VerifyOutcome, 0, len(items))
	for _, item := range items {
		asset, err := s.Verify(ctx, actor, item.AssetID, item.ObservedSum)
		if err != nil {
			public := apierr.From(err)
			status := domain.AssetIngesting
			if current, loadErr := s.store.Assets().GetByID(ctx, item.AssetID); loadErr == nil {
				status = current.Status
			}
			outcomes = append(outcomes, VerifyOutcome{AssetID: item.AssetID, Status: status, Message: public.Message})
			continue
		}
		outcomes = append(outcomes, VerifyOutcome{AssetID: asset.ID, Status: asset.Status, Message: "verified"})
	}
	return outcomes, nil
}

// Quarantine removes footage from editorial use.
func (s *Service) Quarantine(ctx context.Context, actor domain.Principal, assetID, reason string) (*domain.MediaAsset, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "a quarantine reason is required",
			domain.NewValidationError("reason", "must not be empty"))
	}
	now := s.clk.Now()
	var quarantined *domain.MediaAsset
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		asset, err := s.store.Assets().GetByID(txCtx, assetID)
		if err != nil {
			return err
		}
		if _, err := s.loadWritableProject(txCtx, actor, asset.ProjectID); err != nil {
			return err
		}
		if err := asset.Quarantine(reason, now); err != nil {
			return err
		}
		if err := s.store.Assets().Update(txCtx, asset); err != nil {
			return err
		}
		if err := s.recorder.Succeeded(txCtx, actor, domain.ActionAssetQuarantine, audit.ObjectAsset, asset.ID, reason); err != nil {
			return err
		}
		quarantined = asset
		return nil
	})
	if err != nil {
		return nil, s.translate(err, "could not quarantine the footage")
	}
	return quarantined, nil
}

// ExtendRetention pushes the retention deadline of one asset forward.
func (s *Service) ExtendRetention(ctx context.Context, actor domain.Principal, assetID string, by time.Duration) (*domain.MediaAsset, error) {
	now := s.clk.Now()
	var updated *domain.MediaAsset
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		asset, err := s.store.Assets().GetByID(txCtx, assetID)
		if err != nil {
			return err
		}
		if _, err := s.loadWritableProject(txCtx, actor, asset.ProjectID); err != nil {
			return err
		}
		if err := asset.ExtendRetention(by, now); err != nil {
			return err
		}
		if err := s.store.Assets().Update(txCtx, asset); err != nil {
			return err
		}
		updated = asset
		return nil
	})
	if err != nil {
		return nil, s.translate(err, "could not extend the retention window")
	}
	return updated, nil
}

// Get returns one asset if the caller may read the project.
func (s *Service) Get(ctx context.Context, actor domain.Principal, assetID string) (*domain.MediaAsset, error) {
	asset, err := s.store.Assets().GetByID(ctx, assetID)
	if err != nil {
		return nil, s.translate(err, "could not read the footage")
	}
	if _, err := s.loadReadableProject(ctx, actor, asset.ProjectID); err != nil {
		return nil, err
	}
	return asset, nil
}

// List returns a filtered page of assets.
func (s *Service) List(ctx context.Context, actor domain.Principal, filter domain.AssetFilter, page domain.Page) (domain.PageResult[*domain.MediaAsset], error) {
	var empty domain.PageResult[*domain.MediaAsset]
	if strings.TrimSpace(filter.ProjectID) == "" {
		return empty, apierr.Wrap(apierr.CodeInvalidRequest, "a project is required to list footage",
			domain.NewValidationError("project_id", "must not be empty"))
	}
	if _, err := s.loadReadableProject(ctx, actor, filter.ProjectID); err != nil {
		return empty, err
	}
	result, err := s.store.Assets().List(ctx, filter, page)
	if err != nil {
		return empty, apierr.Wrap(apierr.CodeInternal, "could not list footage", err)
	}
	return result, nil
}

func (s *Service) loadWritableProject(ctx context.Context, actor domain.Principal, projectID string) (*domain.Project, error) {
	project, err := s.store.Projects().GetByID(ctx, projectID)
	if err != nil {
		return nil, s.translate(err, "could not read the project")
	}
	if err := project.EnsureWriteAccess(actor); err != nil {
		return nil, apierr.Wrap(apierr.CodeForbidden, "you may not change footage on this project", err)
	}
	if err := project.EnsureEditable(); err != nil {
		return nil, apierr.Wrap(apierr.CodePreconditionFail, "this project no longer accepts footage changes", err)
	}
	return project, nil
}

func (s *Service) loadReadableProject(ctx context.Context, actor domain.Principal, projectID string) (*domain.Project, error) {
	project, err := s.store.Projects().GetByID(ctx, projectID)
	if err != nil {
		return nil, s.translate(err, "could not read the project")
	}
	if actor.IsZero() {
		return nil, apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	if project.OwnedBy(actor) || actor.Role != domain.RoleEditor {
		return project, nil
	}
	return nil, apierr.Wrap(apierr.CodeForbidden, "you may not read this project", domain.ErrPermissionDenied)
}

func (s *Service) translate(err error, message string) error {
	var public *apierr.Error
	if errors.As(err, &public) {
		return public
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return apierr.Wrap(apierr.CodeNotFound, "that record does not exist", err)
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
