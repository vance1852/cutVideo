// Package sqlite implements the repository contracts on top of a real SQLite
// database. Every query is executed against the connection or transaction that
// the caller's context carries, so a service can span several repositories
// inside one atomic unit of work.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/repository"
)

type txKey struct{}

// queryer is the subset of database/sql shared by *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is the SQLite backed store.
type DB struct {
	db     *sql.DB
	logger *slog.Logger

	users       *userRepo
	sessions    *sessionRepo
	projects    *projectRepo
	assets      *assetRepo
	timelines   *timelineRepo
	renders     *renderRepo
	slots       *slotRepo
	deliveries  *deliveryRepo
	audits      *auditRepo
	idempotency *idempotencyRepo
}

// Open connects to SQLite, applies the required pragmas and optionally runs the
// embedded migrations.
func Open(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) (*DB, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("sqlite: dsn must not be empty")
	}
	handle, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %q: %w", cfg.DSN, err)
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen < 1 {
		maxOpen = 1
	}
	handle.SetMaxOpenConns(maxOpen)
	handle.SetMaxIdleConns(maxOpen)
	if cfg.ConnMaxIdleTime > 0 {
		handle.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	}
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := handle.ExecContext(ctx, pragma); err != nil {
			_ = handle.Close()
			return nil, fmt.Errorf("sqlite: apply %q: %w", pragma, err)
		}
	}
	store := &DB{db: handle, logger: logger}
	store.users = &userRepo{store: store}
	store.sessions = &sessionRepo{store: store}
	store.projects = &projectRepo{store: store}
	store.assets = &assetRepo{store: store}
	store.timelines = &timelineRepo{store: store}
	store.renders = &renderRepo{store: store}
	store.slots = &slotRepo{store: store}
	store.deliveries = &deliveryRepo{store: store}
	store.audits = &auditRepo{store: store}
	store.idempotency = &idempotencyRepo{store: store}

	if cfg.RunMigrations {
		if err := store.Migrate(ctx); err != nil {
			_ = handle.Close()
			return nil, err
		}
	}
	return store, nil
}

// Close releases the underlying pool.
func (d *DB) Close() error { return d.db.Close() }

// Ping verifies that the database answers queries.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.db.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite: ping: %w", err)
	}
	var one int
	if err := d.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("sqlite: readiness probe: %w", err)
	}
	return nil
}

// InTx runs fn inside a transaction. Nested calls join the outer transaction so
// service composition never opens a second write transaction by accident. The
// unit of work owns its own lifetime: it is started on a detached context so a
// slow or impatient caller cannot tear the transaction down while statements are
// still in flight.
func (d *DB) InTx(ctx context.Context, fn repository.TxFunc) error {
	if _, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return fn(ctx)
	}
	unitCtx := context.WithoutCancel(ctx)
	tx, err := d.db.BeginTx(unitCtx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin transaction: %w", err)
	}
	txCtx := context.WithValue(unitCtx, txKey{}, tx)
	if err := fn(txCtx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("sqlite: rollback after %v: %w", err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit transaction: %w", err)
	}
	return nil
}

// InTxNoted behaves like InTx and reports whether the work committed.
func (d *DB) InTxNoted(ctx context.Context, fn repository.TxFunc) (bool, error) {
	if err := d.InTx(ctx, fn); err != nil {
		return false, err
	}
	return true, nil
}

func (d *DB) conn(ctx context.Context) queryer {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return d.db
}

// InTransaction reports whether the context is already inside a transaction.
func InTransaction(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(*sql.Tx)
	return ok
}

// Users returns the user repository.
func (d *DB) Users() repository.UserRepository { return d.users }

// Sessions returns the session repository.
func (d *DB) Sessions() repository.SessionRepository { return d.sessions }

// Projects returns the project repository.
func (d *DB) Projects() repository.ProjectRepository { return d.projects }

// Assets returns the media asset repository.
func (d *DB) Assets() repository.AssetRepository { return d.assets }

// Timelines returns the timeline repository.
func (d *DB) Timelines() repository.TimelineRepository { return d.timelines }

// Renders returns the render job repository.
func (d *DB) Renders() repository.RenderRepository { return d.renders }

// Slots returns the render slot repository.
func (d *DB) Slots() repository.SlotRepository { return d.slots }

// Deliveries returns the delivery repository.
func (d *DB) Deliveries() repository.DeliveryRepository { return d.deliveries }

// Audits returns the audit repository.
func (d *DB) Audits() repository.AuditRepository { return d.audits }

// Idempotency returns the idempotency repository.
func (d *DB) Idempotency() repository.IdempotencyRepository { return d.idempotency }

func millis(at time.Time) int64 { return at.UTC().UnixMilli() }

func nullableMillis(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.UTC().UnixMilli()
}

func fromMillis(value int64) time.Time {
	return time.UnixMilli(value).In(businessLocation)
}

func fromNullableMillis(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	stamp := fromMillis(value.Int64)
	return &stamp
}

func translate(err error, entity string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", entity, domain.ErrNotFound)
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "UNIQUE constraint failed"):
		return fmt.Errorf("%s: %w: %v", entity, domain.ErrConflict, err)
	case strings.Contains(message, "FOREIGN KEY constraint failed"):
		return fmt.Errorf("%s: %w: %v", entity, domain.ErrConflict, err)
	case strings.Contains(message, "CHECK constraint failed"):
		return fmt.Errorf("%s: %w: %v", entity, domain.ErrValidation, err)
	default:
		return fmt.Errorf("%s: %w", entity, err)
	}
}

func orderClause(page domain.Page, allowed map[string]string, fallback string) string {
	column := fallback
	if page.SortBy != "" {
		if mapped, ok := allowed[page.SortBy]; ok {
			column = mapped
		}
	}
	direction := "DESC"
	if !page.Descending() {
		direction = "ASC"
	}
	return fmt.Sprintf("ORDER BY %s %s, id %s", column, direction, direction)
}
