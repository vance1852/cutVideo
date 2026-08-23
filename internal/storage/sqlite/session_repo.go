package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

type sessionRepo struct {
	store *DB
}

const sessionColumns = "id, user_id, token_hash, user_agent, issued_at, expires_at, last_seen_at, revoked_at"

func (r *sessionRepo) Create(ctx context.Context, session *domain.Session) error {
	if session == nil {
		return domain.NewValidationError("session", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO sessions ("+sessionColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		session.ID, session.UserID, session.TokenHash, session.UserAgent,
		millis(session.IssuedAt), millis(session.ExpiresAt), millis(session.LastSeenAt),
		nullableMillis(session.RevokedAt),
	)
	return translate(err, "sessions.create")
}

func (r *sessionRepo) Update(ctx context.Context, session *domain.Session) error {
	if session == nil {
		return domain.NewValidationError("session", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE sessions SET expires_at = ?, last_seen_at = ?, revoked_at = ? WHERE id = ?`,
		millis(session.ExpiresAt), millis(session.LastSeenAt), nullableMillis(session.RevokedAt), session.ID,
	)
	if err != nil {
		return translate(err, "sessions.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sessions.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("sessions.update: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *sessionRepo) GetByID(ctx context.Context, id string) (*domain.Session, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM sessions WHERE id = ?", id)
	return scanSession(row)
}

func (r *sessionRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM sessions WHERE token_hash = ?", tokenHash)
	return scanSession(row)
}

func (r *sessionRepo) RevokeAllForUser(ctx context.Context, userID string, at time.Time) (int, error) {
	result, err := r.store.conn(ctx).ExecContext(ctx,
		"UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL",
		millis(at), userID,
	)
	if err != nil {
		return 0, translate(err, "sessions.revoke_all")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sessions.revoke_all: rows affected: %w", err)
	}
	return int(affected), nil
}

func (r *sessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	result, err := r.store.conn(ctx).ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", millis(before))
	if err != nil {
		return 0, translate(err, "sessions.delete_expired")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sessions.delete_expired: rows affected: %w", err)
	}
	return int(affected), nil
}

func scanSession(row scannable) (*domain.Session, error) {
	var (
		session    domain.Session
		issuedAt   int64
		expiresAt  int64
		lastSeenAt int64
		revokedAt  sql.NullInt64
	)
	if err := row.Scan(&session.ID, &session.UserID, &session.TokenHash, &session.UserAgent,
		&issuedAt, &expiresAt, &lastSeenAt, &revokedAt); err != nil {
		return nil, translate(err, "sessions.get")
	}
	session.IssuedAt = fromMillis(issuedAt)
	session.ExpiresAt = fromMillis(expiresAt)
	session.LastSeenAt = fromMillis(lastSeenAt)
	session.RevokedAt = fromNullableMillis(revokedAt)
	return &session, nil
}
