package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

type renderRepo struct {
	store *DB
}

const renderColumns = "id, project_id, timeline_id, requested_by, preset, priority, status, attempt, max_attempts, slot_id, lease_expires_at, next_attempt_at, queued_at, started_at, finished_at, last_error, output_uri, output_bytes, idempotency_key, row_version, created_at, updated_at"

func (r *renderRepo) Create(ctx context.Context, job *domain.RenderJob) error {
	if job == nil {
		return domain.NewValidationError("render_job", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO render_jobs ("+renderColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		job.ID, job.ProjectID, job.TimelineID, job.RequestedBy, job.Preset, int(job.Priority),
		string(job.Status), job.Attempt, job.MaxAttempts, job.SlotID,
		nullableMillis(job.LeaseExpiresAt), millis(job.NextAttemptAt), millis(job.QueuedAt),
		nullableMillis(job.StartedAt), nullableMillis(job.FinishedAt), job.LastError,
		job.OutputURI, job.OutputBytes, job.IdempotencyKey, job.RowVersion,
		millis(job.CreatedAt), millis(job.UpdatedAt),
	)
	return translate(err, "renders.create")
}

// Update writes a render job with an optimistic guard on row_version so two
// workers cannot both advance the same job.
func (r *renderRepo) Update(ctx context.Context, job *domain.RenderJob, expectedRowVersion int) error {
	if job == nil {
		return domain.NewValidationError("render_job", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE render_jobs SET status = ?, attempt = ?, slot_id = ?, lease_expires_at = ?,
		        next_attempt_at = ?, started_at = ?, finished_at = ?, last_error = ?,
		        output_uri = ?, output_bytes = ?, priority = ?, row_version = ?, updated_at = ?
		 WHERE id = ? AND row_version = ?`,
		string(job.Status), job.Attempt, job.SlotID, nullableMillis(job.LeaseExpiresAt),
		millis(job.NextAttemptAt), nullableMillis(job.StartedAt), nullableMillis(job.FinishedAt),
		job.LastError, job.OutputURI, job.OutputBytes, int(job.Priority), job.RowVersion,
		millis(job.UpdatedAt), job.ID, expectedRowVersion,
	)
	if err != nil {
		return translate(err, "renders.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("renders.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("renders.update %s: %w", job.ID, domain.ErrVersionConflict)
	}
	return nil
}

func (r *renderRepo) GetByID(ctx context.Context, id string) (*domain.RenderJob, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+renderColumns+" FROM render_jobs WHERE id = ?", id)
	return scanRender(row)
}

func (r *renderRepo) FindByIdempotencyKey(ctx context.Context, projectID, key string) (*domain.RenderJob, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return nil, fmt.Errorf("renders.find_by_key: %w", domain.ErrNotFound)
	}
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+renderColumns+" FROM render_jobs WHERE project_id = ? AND idempotency_key = ?",
		projectID, trimmed,
	)
	return scanRender(row)
}

func (r *renderRepo) CountActiveForTimeline(ctx context.Context, timelineID string) (int, error) {
	var total int
	err := r.store.conn(ctx).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM render_jobs WHERE timeline_id = ? AND status IN (?, ?, ?)`,
		timelineID, string(domain.RenderQueued), string(domain.RenderAssigned), string(domain.RenderRendering),
	).Scan(&total)
	if err != nil {
		return 0, translate(err, "renders.count_active")
	}
	return total, nil
}

// NextEligible returns queued jobs whose backoff window elapsed, ordered by
// priority and then by queue arrival.
func (r *renderRepo) NextEligible(ctx context.Context, now time.Time, limit int) ([]*domain.RenderJob, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := r.store.conn(ctx).QueryContext(ctx,
		"SELECT "+renderColumns+` FROM render_jobs
		 WHERE status = ? AND next_attempt_at <= ?
		 ORDER BY priority DESC, queued_at ASC, id ASC LIMIT ?`,
		string(domain.RenderQueued), millis(now), limit,
	)
	if err != nil {
		return nil, translate(err, "renders.next_eligible")
	}
	defer rows.Close()
	return collectRenders(rows, limit)
}

// ListExpiredLeases returns leased jobs whose lease deadline elapsed.
func (r *renderRepo) ListExpiredLeases(ctx context.Context, now time.Time, limit int) ([]*domain.RenderJob, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := r.store.conn(ctx).QueryContext(ctx,
		"SELECT "+renderColumns+` FROM render_jobs
		 WHERE status IN (?, ?) AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
		 ORDER BY lease_expires_at ASC LIMIT ?`,
		string(domain.RenderAssigned), string(domain.RenderRendering), millis(now), limit,
	)
	if err != nil {
		return nil, translate(err, "renders.expired_leases")
	}
	defer rows.Close()
	return collectRenders(rows, limit)
}

func (r *renderRepo) List(ctx context.Context, filter domain.RenderFilter, page domain.Page) (domain.PageResult[*domain.RenderJob], error) {
	var empty domain.PageResult[*domain.RenderJob]
	where, args := renderFilterClause(filter)
	conn := r.store.conn(ctx)

	var total int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM render_jobs "+where, args...).Scan(&total); err != nil {
		return empty, translate(err, "renders.count")
	}
	order := orderClause(page, map[string]string{
		"queued_at":  "queued_at",
		"created_at": "created_at",
		"priority":   "priority",
		"status":     "status",
	}, "queued_at")
	listArgs := append(append([]any{}, args...), page.Limit(), page.Offset())
	rows, err := conn.QueryContext(ctx,
		"SELECT "+renderColumns+" FROM render_jobs "+where+" "+order+" LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return empty, translate(err, "renders.list")
	}
	defer rows.Close()

	items, err := collectRenders(rows, page.Limit())
	if err != nil {
		return empty, err
	}
	return domain.NewPageResult(items, total, page), nil
}

// renderFilterClause builds the WHERE fragment shared by the count query and the
// page query.
func renderFilterClause(filter domain.RenderFilter) (string, []any) {
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if strings.TrimSpace(filter.ProjectID) != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, filter.ProjectID)
	}
	if strings.TrimSpace(filter.TimelineID) != "" {
		conditions = append(conditions, "timeline_id = ?")
		args = append(args, filter.TimelineID)
	}
	if strings.TrimSpace(string(filter.Status)) != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}
	if strings.TrimSpace(filter.Preset) != "" {
		conditions = append(conditions, "preset = ?")
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Preset)))
	}
	if strings.TrimSpace(filter.RequestedBy) != "" {
		conditions = append(conditions, "requested_by = ?")
		args = append(args, filter.RequestedBy)
	}
	if filter.OnlyActive {
		conditions = append(conditions, "status IN (?, ?, ?)")
		args = append(args, string(domain.RenderQueued), string(domain.RenderAssigned), string(domain.RenderRendering))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func collectRenders(rows *sql.Rows, capacity int) ([]*domain.RenderJob, error) {
	if capacity < 1 {
		capacity = 8
	}
	out := make([]*domain.RenderJob, 0, capacity)
	for rows.Next() {
		job, err := scanRender(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "renders.iterate")
	}
	return out, nil
}

func scanRender(row scannable) (*domain.RenderJob, error) {
	var (
		job        domain.RenderJob
		priority   int
		status     string
		lease      sql.NullInt64
		nextAt     int64
		queuedAt   int64
		startedAt  sql.NullInt64
		finishedAt sql.NullInt64
		createdAt  int64
		updatedAt  int64
	)
	if err := row.Scan(&job.ID, &job.ProjectID, &job.TimelineID, &job.RequestedBy, &job.Preset,
		&priority, &status, &job.Attempt, &job.MaxAttempts, &job.SlotID, &lease, &nextAt,
		&queuedAt, &startedAt, &finishedAt, &job.LastError, &job.OutputURI, &job.OutputBytes,
		&job.IdempotencyKey, &job.RowVersion, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "renders.get")
	}
	job.Priority = domain.RenderPriority(priority)
	job.Status = domain.RenderStatus(status)
	job.LeaseExpiresAt = fromNullableMillis(lease)
	job.NextAttemptAt = fromMillis(nextAt)
	job.QueuedAt = fromMillis(queuedAt)
	job.StartedAt = fromNullableMillis(startedAt)
	job.FinishedAt = fromNullableMillis(finishedAt)
	job.CreatedAt = fromMillis(createdAt)
	job.UpdatedAt = fromMillis(updatedAt)
	return &job, nil
}
