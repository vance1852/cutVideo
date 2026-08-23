// Package auth owns operator identity: account provisioning, sign in, session
// validation, sign out and administrative revocation.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/audit"
	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/repository"
)

// Service implements the identity use cases.
type Service struct {
	store    repository.Store
	recorder *audit.Recorder
	gen      ids.Generator
	clk      clock.Clock
	cfg      config.AuthConfig
	logger   *slog.Logger
}

// New builds the auth service.
func New(store repository.Store, recorder *audit.Recorder, gen ids.Generator, clk clock.Clock, cfg config.AuthConfig, logger *slog.Logger) *Service {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Service{store: store, recorder: recorder, gen: gen, clk: clk, cfg: cfg, logger: logger}
}

// ProvisionInput describes a new operator account.
type ProvisionInput struct {
	Email       string
	DisplayName string
	Role        domain.Role
	Password    string
}

// Provision creates an operator account. It is used by the bootstrap path and by
// supervisors adding colleagues.
func (s *Service) Provision(ctx context.Context, actor domain.Principal, input ProvisionInput) (*domain.User, error) {
	if !actor.IsZero() && !actor.Role.CanArbitrateFarm() {
		return nil, apierr.Wrap(apierr.CodeForbidden, "only a supervisor may create accounts", domain.ErrPermissionDenied)
	}
	if len(strings.TrimSpace(input.Password)) < 10 {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "password must have at least 10 characters",
			domain.NewValidationError("password", "must have at least 10 characters"))
	}
	hash, err := s.hashPassword(input.Password)
	if err != nil {
		return nil, err
	}
	now := s.clk.Now()
	user, err := domain.NewUser(s.gen.NewID("usr"), input.Email, input.DisplayName, input.Role, hash, now)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInvalidRequest, "account details are not acceptable", err)
	}
	err = s.store.InTx(ctx, func(txCtx context.Context) error {
		if err := s.store.Users().Create(txCtx, user); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, "auth.provision", audit.ObjectSession, user.ID,
			"role="+string(user.Role))
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, apierr.Wrap(apierr.CodeConflict, "that email address already has an account", err)
		}
		return nil, apierr.Wrap(apierr.CodeInternal, "could not create the account", err)
	}
	return user, nil
}

// SignInInput carries sign in credentials.
type SignInInput struct {
	Email     string
	Password  string
	UserAgent string
}

// SignInResult carries the freshly minted bearer token. The raw token is only
// ever returned here; the database stores its hash.
type SignInResult struct {
	Token     string
	ExpiresAt time.Time
	User      *domain.User
	SessionID string
}

// SignIn validates credentials and opens a revocable session.
func (s *Service) SignIn(ctx context.Context, input SignInInput) (*SignInResult, error) {
	user, err := s.store.Users().GetByEmail(ctx, input.Email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrValidation) {
			return nil, apierr.Wrap(apierr.CodeUnauthenticated, "email or password is incorrect", domain.ErrCredentialsRejected)
		}
		return nil, apierr.Wrap(apierr.CodeInternal, "could not read the account", err)
	}
	if !user.Active() {
		return nil, apierr.Wrap(apierr.CodeForbidden, "this account is suspended", domain.ErrPermissionDenied)
	}
	if err := s.verifyPassword(user.PasswordHash, input.Password); err != nil {
		_ = s.store.InTx(ctx, func(txCtx context.Context) error {
			return s.recorder.Rejected(txCtx, domain.Principal{UserID: user.ID, Role: user.Role},
				domain.ActionSignIn, audit.ObjectSession, user.ID, "credentials rejected")
		})
		return nil, apierr.Wrap(apierr.CodeUnauthenticated, "email or password is incorrect", domain.ErrCredentialsRejected)
	}

	token, err := s.gen.NewToken()
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternal, "could not mint a session token", err)
	}
	now := s.clk.Now()
	session, err := domain.NewSession(s.gen.NewID("ses"), user.ID, ids.HashToken(token), input.UserAgent, now, s.cfg.SessionTTL)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternal, "could not build the session", err)
	}
	principal := domain.Principal{UserID: user.ID, SessionID: session.ID, Role: user.Role, Email: user.Email}
	err = s.store.InTx(ctx, func(txCtx context.Context) error {
		if err := s.store.Sessions().Create(txCtx, session); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, principal, domain.ActionSignIn, audit.ObjectSession, session.ID, "session opened")
	})
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternal, "could not open the session", err)
	}
	logging.FromContext(ctx, s.logger).Info("operator signed in", "user_id", user.ID, "role", string(user.Role))
	return &SignInResult{Token: token, ExpiresAt: session.ExpiresAt, User: user, SessionID: session.ID}, nil
}

// Authenticate resolves a bearer token into a principal.
func (s *Service) Authenticate(ctx context.Context, token string) (domain.Principal, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return domain.Principal{}, apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	session, err := s.store.Sessions().GetByTokenHash(ctx, ids.HashToken(trimmed))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Principal{}, apierr.Wrap(apierr.CodeUnauthenticated, "this session is not valid", err)
		}
		return domain.Principal{}, apierr.Wrap(apierr.CodeInternal, "could not read the session", err)
	}
	now := s.clk.Now()
	if err := session.EnsureUsable(now); err != nil {
		switch {
		case errors.Is(err, domain.ErrSessionExpired):
			return domain.Principal{}, apierr.Wrap(apierr.CodeUnauthenticated, "this session has expired, sign in again", err)
		case errors.Is(err, domain.ErrSessionRevoked):
			return domain.Principal{}, apierr.Wrap(apierr.CodeUnauthenticated, "this session was revoked", err)
		default:
			return domain.Principal{}, apierr.Wrap(apierr.CodeUnauthenticated, "this session is not valid", err)
		}
	}
	user, err := s.store.Users().GetByID(ctx, session.UserID)
	if err != nil {
		return domain.Principal{}, apierr.Wrap(apierr.CodeUnauthenticated, "the account behind this session is gone", err)
	}
	if !user.Active() {
		return domain.Principal{}, apierr.Wrap(apierr.CodeForbidden, "this account is suspended", domain.ErrPermissionDenied)
	}
	session.Touch(now)
	if err := s.store.Sessions().Update(ctx, session); err != nil {
		return domain.Principal{}, apierr.Wrap(apierr.CodeInternal, "could not refresh the session", err)
	}
	return domain.Principal{UserID: user.ID, SessionID: session.ID, Role: user.Role, Email: user.Email}, nil
}

// SignOut revokes the caller's own session.
func (s *Service) SignOut(ctx context.Context, actor domain.Principal) error {
	if actor.IsZero() {
		return apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	now := s.clk.Now()
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		session, err := s.store.Sessions().GetByID(txCtx, actor.SessionID)
		if err != nil {
			return err
		}
		if err := session.Revoke(now); err != nil {
			return err
		}
		if err := s.store.Sessions().Update(txCtx, session); err != nil {
			return err
		}
		return s.recorder.Succeeded(txCtx, actor, domain.ActionSignOut, audit.ObjectSession, session.ID, "session revoked")
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return apierr.Wrap(apierr.CodeNotFound, "this session no longer exists", err)
		}
		if errors.Is(err, domain.ErrInvalidTransition) {
			return apierr.Wrap(apierr.CodeConflict, "this session was already signed out", err)
		}
		return apierr.Wrap(apierr.CodeInternal, "could not sign out", err)
	}
	return nil
}

// RevokeAllForUser signs every session of one operator out.
func (s *Service) RevokeAllForUser(ctx context.Context, actor domain.Principal, userID string) (int, error) {
	if err := actor.RequireFarmArbitration(); err != nil {
		return 0, apierr.Wrap(apierr.CodeForbidden, "only a supervisor may revoke other sessions", err)
	}
	now := s.clk.Now()
	var revoked int
	err := s.store.InTx(ctx, func(txCtx context.Context) error {
		count, err := s.store.Sessions().RevokeAllForUser(txCtx, userID, now)
		if err != nil {
			return err
		}
		revoked = count
		return s.recorder.Succeeded(txCtx, actor, domain.ActionSignOut, audit.ObjectSession, userID,
			fmt.Sprintf("revoked=%d", count))
	})
	if err != nil {
		return 0, apierr.Wrap(apierr.CodeInternal, "could not revoke the sessions", err)
	}
	return revoked, nil
}

// PurgeExpiredSessions deletes sessions that expired before now.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int, error) {
	return s.store.Sessions().DeleteExpired(ctx, s.clk.Now())
}

// ListUsers returns operators for supervisors and auditors.
func (s *Service) ListUsers(ctx context.Context, actor domain.Principal, page domain.Page) (domain.PageResult[*domain.User], error) {
	var empty domain.PageResult[*domain.User]
	if actor.IsZero() {
		return empty, apierr.New(apierr.CodeUnauthenticated, "a session token is required")
	}
	if actor.Role == domain.RoleEditor {
		return empty, apierr.Wrap(apierr.CodeForbidden, "editors may not list operators", domain.ErrPermissionDenied)
	}
	result, err := s.store.Users().List(ctx, page)
	if err != nil {
		return empty, apierr.Wrap(apierr.CodeInternal, "could not list operators", err)
	}
	return result, nil
}

func (s *Service) hashPassword(password string) (string, error) {
	material := []byte(strings.TrimSpace(password) + s.cfg.PasswordPepper)
	hash, err := bcrypt.GenerateFromPassword(material, bcrypt.DefaultCost)
	if err != nil {
		return "", apierr.Wrap(apierr.CodeInternal, "could not hash the password", err)
	}
	return string(hash), nil
}

func (s *Service) verifyPassword(hash, password string) error {
	material := []byte(strings.TrimSpace(password) + s.cfg.PasswordPepper)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), material); err != nil {
		return domain.ErrCredentialsRejected
	}
	return nil
}
