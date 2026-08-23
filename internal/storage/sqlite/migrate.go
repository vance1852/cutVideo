package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/migrations"
)

var businessLocation = clock.BusinessLocation

const schemaTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at INTEGER NOT NULL
)`

// AppliedMigration describes one schema step already present in the database.
type AppliedMigration struct {
	Version   int
	Name      string
	AppliedAt time.Time
}

// Migrate brings the database to the latest embedded schema version. Running it
// repeatedly is safe: already applied versions are skipped, and a database that
// carries an unknown newer version is reported instead of being modified.
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, schemaTableDDL); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}
	pending, err := migrations.All()
	if err != nil {
		return err
	}
	applied, err := d.AppliedMigrations(ctx)
	if err != nil {
		return err
	}
	known := map[int]AppliedMigration{}
	highestEmbedded := pending[len(pending)-1].Version
	for _, record := range applied {
		known[record.Version] = record
		if record.Version > highestEmbedded {
			return fmt.Errorf("sqlite: database schema version %d is newer than this binary supports (%d)", record.Version, highestEmbedded)
		}
	}
	for _, migration := range pending {
		if existing, ok := known[migration.Version]; ok {
			if existing.Name != migration.Name {
				return fmt.Errorf("sqlite: migration %d was applied as %q but this binary carries %q", migration.Version, existing.Name, migration.Name)
			}
			continue
		}
		if err := d.applyMigration(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) applyMigration(ctx context.Context, migration migrations.Migration) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin migration %d: %w", migration.Version, err)
	}
	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("sqlite: rollback migration %d after %v: %w", migration.Version, err, rollbackErr)
		}
		return fmt.Errorf("sqlite: apply migration %d (%s): %w", migration.Version, migration.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
		migration.Version, migration.Name, time.Now().UTC().UnixMilli(),
	); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("sqlite: rollback migration bookkeeping %d after %v: %w", migration.Version, err, rollbackErr)
		}
		return fmt.Errorf("sqlite: record migration %d: %w", migration.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit migration %d: %w", migration.Version, err)
	}
	if d.logger != nil {
		d.logger.Info("schema migration applied", "version", migration.Version, "name", migration.Name)
	}
	return nil
}

// AppliedMigrations lists the schema steps recorded in the database.
func (d *DB) AppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT version, name, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make([]AppliedMigration, 0, 8)
	for rows.Next() {
		var record AppliedMigration
		var appliedAt int64
		if err := rows.Scan(&record.Version, &record.Name, &appliedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan schema_migrations: %w", err)
		}
		record.AppliedAt = fromMillis(appliedAt)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate schema_migrations: %w", err)
	}
	return out, nil
}

// SchemaVersion reports the highest applied schema version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	applied, err := d.AppliedMigrations(ctx)
	if err != nil {
		return 0, err
	}
	if len(applied) == 0 {
		return 0, nil
	}
	return applied[len(applied)-1].Version, nil
}
