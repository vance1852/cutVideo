package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vance1852/cutVideo/internal/domain"
)

type timelineRepo struct {
	store *DB
}

const timelineColumns = "id, project_id, version, status, notes, created_by, clip_count, total_duration_ms, row_version, sealed_at, created_at, updated_at"

const clipColumns = "id, timeline_id, asset_id, order_index, source_in_ms, source_out_ms, track, transition, speed_percent, created_at, updated_at"

func (r *timelineRepo) CreateVersion(ctx context.Context, version *domain.TimelineVersion) error {
	if version == nil {
		return domain.NewValidationError("timeline", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO timeline_versions ("+timelineColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		version.ID, version.ProjectID, version.Version, string(version.Status), version.Notes,
		version.CreatedBy, version.ClipCount, version.TotalDurationMS, version.RowVersion,
		nullableMillis(version.SealedAt), millis(version.CreatedAt), millis(version.UpdatedAt),
	)
	return translate(err, "timelines.create")
}

// UpdateVersion applies an optimistic update. The row version guard makes a
// concurrent seal or clip mutation fail instead of silently overwriting the
// other writer's result.
func (r *timelineRepo) UpdateVersion(ctx context.Context, version *domain.TimelineVersion, expectedRowVersion int) error {
	if version == nil {
		return domain.NewValidationError("timeline", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE timeline_versions SET status = ?, notes = ?, clip_count = ?, total_duration_ms = ?,
		        row_version = ?, sealed_at = ?, updated_at = ?
		 WHERE id = ? AND row_version = ?`,
		string(version.Status), version.Notes, version.ClipCount, version.TotalDurationMS,
		version.RowVersion, nullableMillis(version.SealedAt), millis(version.UpdatedAt),
		version.ID, expectedRowVersion,
	)
	if err != nil {
		return translate(err, "timelines.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("timelines.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("timelines.update %s: %w", version.ID, domain.ErrVersionConflict)
	}
	return nil
}

func (r *timelineRepo) GetVersion(ctx context.Context, id string) (*domain.TimelineVersion, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+timelineColumns+" FROM timeline_versions WHERE id = ?", id)
	version, err := scanTimeline(row)
	if err != nil {
		return nil, err
	}
	clips, err := r.ListClips(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	version.AttachClips(clips)
	return version, nil
}

func (r *timelineRepo) GetVersionByNumber(ctx context.Context, projectID string, number int) (*domain.TimelineVersion, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+timelineColumns+" FROM timeline_versions WHERE project_id = ? AND version = ?",
		projectID, number,
	)
	version, err := scanTimeline(row)
	if err != nil {
		return nil, err
	}
	clips, err := r.ListClips(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	version.AttachClips(clips)
	return version, nil
}

func (r *timelineRepo) LatestSealed(ctx context.Context, projectID string) (*domain.TimelineVersion, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+timelineColumns+" FROM timeline_versions WHERE project_id = ? AND status = ? ORDER BY version DESC LIMIT 1",
		projectID, string(domain.TimelineSealed),
	)
	version, err := scanTimeline(row)
	if err != nil {
		return nil, err
	}
	clips, err := r.ListClips(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	version.AttachClips(clips)
	return version, nil
}

// CurrentSealed resolves the sealed cut a project currently advertises by
// joining the project's sealed pointer with the timeline it names. Reading the
// pointer from the database keeps the lookup stable inside the caller's
// transaction: while a new draft is being sealed the pointer still names the
// previous cut, so the version to supersede is returned instead of the one that
// was just sealed.
func (r *timelineRepo) CurrentSealed(ctx context.Context, projectID string) (*domain.TimelineVersion, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+timelineColumns+` FROM timeline_versions
		 WHERE project_id = ? AND version = (SELECT sealed_version FROM projects WHERE id = ?)`,
		projectID, projectID,
	)
	version, err := scanTimeline(row)
	if err != nil {
		return nil, err
	}
	clips, err := r.ListClips(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	version.AttachClips(clips)
	return version, nil
}

func (r *timelineRepo) List(ctx context.Context, filter domain.TimelineFilter, page domain.Page) (domain.PageResult[*domain.TimelineVersion], error) {
	var empty domain.PageResult[*domain.TimelineVersion]
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if strings.TrimSpace(filter.ProjectID) != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, filter.ProjectID)
	}
	if strings.TrimSpace(string(filter.Status)) != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	conn := r.store.conn(ctx)
	var total int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM timeline_versions "+where, args...).Scan(&total); err != nil {
		return empty, translate(err, "timelines.count")
	}
	order := orderClause(page, map[string]string{
		"version":    "version",
		"created_at": "created_at",
	}, "version")
	listArgs := append(append([]any{}, args...), page.Limit(), page.Offset())
	rows, err := conn.QueryContext(ctx,
		"SELECT "+timelineColumns+" FROM timeline_versions "+where+" "+order+" LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return empty, translate(err, "timelines.list")
	}
	defer rows.Close()

	items := make([]*domain.TimelineVersion, 0, page.Limit())
	for rows.Next() {
		version, err := scanTimeline(rows)
		if err != nil {
			return empty, err
		}
		items = append(items, version)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "timelines.list")
	}
	return domain.NewPageResult(items, total, page), nil
}

func (r *timelineRepo) AddClip(ctx context.Context, clip *domain.Clip) error {
	if clip == nil {
		return domain.NewValidationError("clip", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO timeline_clips ("+clipColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		clip.ID, clip.TimelineID, clip.AssetID, clip.OrderIndex, clip.SourceInMS, clip.SourceOutMS,
		string(clip.Track), clip.Transition, clip.SpeedPercent, millis(clip.CreatedAt), millis(clip.UpdatedAt),
	)
	return translate(err, "clips.create")
}

func (r *timelineRepo) RemoveClip(ctx context.Context, timelineID, clipID string) error {
	result, err := r.store.conn(ctx).ExecContext(ctx,
		"DELETE FROM timeline_clips WHERE timeline_id = ? AND id = ?", timelineID, clipID,
	)
	if err != nil {
		return translate(err, "clips.delete")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("clips.delete: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("clips.delete: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *timelineRepo) ListClips(ctx context.Context, timelineID string) ([]*domain.Clip, error) {
	rows, err := r.store.conn(ctx).QueryContext(ctx,
		"SELECT "+clipColumns+" FROM timeline_clips WHERE timeline_id = ? ORDER BY track ASC, order_index ASC",
		timelineID,
	)
	if err != nil {
		return nil, translate(err, "clips.list")
	}
	defer rows.Close()

	out := make([]*domain.Clip, 0, 16)
	for rows.Next() {
		clip, err := scanClip(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, clip)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "clips.list")
	}
	return out, nil
}

func scanTimeline(row scannable) (*domain.TimelineVersion, error) {
	var (
		version   domain.TimelineVersion
		status    string
		sealedAt  sql.NullInt64
		createdAt int64
		updatedAt int64
	)
	if err := row.Scan(&version.ID, &version.ProjectID, &version.Version, &status, &version.Notes,
		&version.CreatedBy, &version.ClipCount, &version.TotalDurationMS, &version.RowVersion,
		&sealedAt, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "timelines.get")
	}
	version.Status = domain.TimelineStatus(status)
	version.SealedAt = fromNullableMillis(sealedAt)
	version.CreatedAt = fromMillis(createdAt)
	version.UpdatedAt = fromMillis(updatedAt)
	return &version, nil
}

func scanClip(row scannable) (*domain.Clip, error) {
	var (
		clip      domain.Clip
		track     string
		createdAt int64
		updatedAt int64
	)
	if err := row.Scan(&clip.ID, &clip.TimelineID, &clip.AssetID, &clip.OrderIndex,
		&clip.SourceInMS, &clip.SourceOutMS, &track, &clip.Transition, &clip.SpeedPercent,
		&createdAt, &updatedAt); err != nil {
		return nil, translate(err, "clips.get")
	}
	clip.Track = domain.Track(track)
	clip.CreatedAt = fromMillis(createdAt)
	clip.UpdatedAt = fromMillis(updatedAt)
	return &clip, nil
}
