package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vance1852/cutVideo/internal/domain"
)

type deliveryRepo struct {
	store *DB
}

const targetColumns = "id, project_id, name, kind, endpoint, credential_ref, status, created_at, updated_at"

const recordColumns = "id, job_id, target_id, project_id, status, attempt, max_attempts, output_uri, failure, dispatched_at, confirmed_at, created_at, updated_at"

func (r *deliveryRepo) CreateTarget(ctx context.Context, target *domain.DeliveryTarget) error {
	if target == nil {
		return domain.NewValidationError("delivery_target", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO delivery_targets ("+targetColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		target.ID, target.ProjectID, target.Name, string(target.Kind), target.Endpoint,
		target.CredentialRef, string(target.Status), millis(target.CreatedAt), millis(target.UpdatedAt),
	)
	return translate(err, "targets.create")
}

func (r *deliveryRepo) UpdateTarget(ctx context.Context, target *domain.DeliveryTarget) error {
	if target == nil {
		return domain.NewValidationError("delivery_target", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE delivery_targets SET name = ?, kind = ?, endpoint = ?, credential_ref = ?, status = ?, updated_at = ?
		 WHERE id = ?`,
		target.Name, string(target.Kind), target.Endpoint, target.CredentialRef,
		string(target.Status), millis(target.UpdatedAt), target.ID,
	)
	if err != nil {
		return translate(err, "targets.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("targets.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("targets.update: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *deliveryRepo) GetTarget(ctx context.Context, id string) (*domain.DeliveryTarget, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+targetColumns+" FROM delivery_targets WHERE id = ?", id)
	return scanTarget(row)
}

func (r *deliveryRepo) GetTargetByName(ctx context.Context, projectID, name string) (*domain.DeliveryTarget, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+targetColumns+" FROM delivery_targets WHERE project_id = ? AND name = ?",
		projectID, strings.TrimSpace(name),
	)
	return scanTarget(row)
}

func (r *deliveryRepo) ListTargets(ctx context.Context, projectID string, onlyEnabled bool) ([]*domain.DeliveryTarget, error) {
	query := "SELECT " + targetColumns + " FROM delivery_targets WHERE project_id = ?"
	args := []any{projectID}
	if onlyEnabled {
		query += " AND status = ?"
		args = append(args, string(domain.TargetEnabled))
	}
	query += " ORDER BY name ASC"
	rows, err := r.store.conn(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, translate(err, "targets.list")
	}
	defer rows.Close()

	out := make([]*domain.DeliveryTarget, 0, 4)
	for rows.Next() {
		target, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, target)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "targets.list")
	}
	return out, nil
}

func (r *deliveryRepo) CreateRecord(ctx context.Context, record *domain.DeliveryRecord) error {
	if record == nil {
		return domain.NewValidationError("delivery_record", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO delivery_records ("+recordColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		record.ID, record.JobID, record.TargetID, record.ProjectID, string(record.Status),
		record.Attempt, record.MaxAttempts, record.OutputURI, record.Failure,
		nullableMillis(record.DispatchedAt), nullableMillis(record.ConfirmedAt),
		millis(record.CreatedAt), millis(record.UpdatedAt),
	)
	return translate(err, "delivery_records.create")
}

func (r *deliveryRepo) UpdateRecord(ctx context.Context, record *domain.DeliveryRecord) error {
	if record == nil {
		return domain.NewValidationError("delivery_record", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE delivery_records SET status = ?, attempt = ?, output_uri = ?, failure = ?,
		        dispatched_at = ?, confirmed_at = ?, updated_at = ?
		 WHERE id = ?`,
		string(record.Status), record.Attempt, record.OutputURI, record.Failure,
		nullableMillis(record.DispatchedAt), nullableMillis(record.ConfirmedAt),
		millis(record.UpdatedAt), record.ID,
	)
	if err != nil {
		return translate(err, "delivery_records.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delivery_records.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("delivery_records.update: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *deliveryRepo) GetRecord(ctx context.Context, jobID, targetID string) (*domain.DeliveryRecord, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+recordColumns+" FROM delivery_records WHERE job_id = ? AND target_id = ?",
		jobID, targetID,
	)
	return scanRecord(row)
}

func (r *deliveryRepo) ListRecordsForJob(ctx context.Context, jobID string) ([]*domain.DeliveryRecord, error) {
	rows, err := r.store.conn(ctx).QueryContext(ctx,
		"SELECT "+recordColumns+" FROM delivery_records WHERE job_id = ? ORDER BY created_at ASC, id ASC",
		jobID,
	)
	if err != nil {
		return nil, translate(err, "delivery_records.list")
	}
	defer rows.Close()

	out := make([]*domain.DeliveryRecord, 0, 4)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "delivery_records.list")
	}
	return out, nil
}

func scanTarget(row scannable) (*domain.DeliveryTarget, error) {
	var (
		target    domain.DeliveryTarget
		kind      string
		status    string
		createdAt int64
		updatedAt int64
	)
	if err := row.Scan(&target.ID, &target.ProjectID, &target.Name, &kind, &target.Endpoint,
		&target.CredentialRef, &status, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "targets.get")
	}
	target.Kind = domain.DeliveryKind(kind)
	target.Status = domain.DeliveryTargetStatus(status)
	target.CreatedAt = fromMillis(createdAt)
	target.UpdatedAt = fromMillis(updatedAt)
	return &target, nil
}

func scanRecord(row scannable) (*domain.DeliveryRecord, error) {
	var (
		record       domain.DeliveryRecord
		status       string
		dispatchedAt sql.NullInt64
		confirmedAt  sql.NullInt64
		createdAt    int64
		updatedAt    int64
	)
	if err := row.Scan(&record.ID, &record.JobID, &record.TargetID, &record.ProjectID, &status,
		&record.Attempt, &record.MaxAttempts, &record.OutputURI, &record.Failure,
		&dispatchedAt, &confirmedAt, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "delivery_records.get")
	}
	record.Status = domain.DeliveryStatus(status)
	record.DispatchedAt = fromNullableMillis(dispatchedAt)
	record.ConfirmedAt = fromNullableMillis(confirmedAt)
	record.CreatedAt = fromMillis(createdAt)
	record.UpdatedAt = fromMillis(updatedAt)
	return &record, nil
}
