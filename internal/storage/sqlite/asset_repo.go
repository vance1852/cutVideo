package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vance1852/cutVideo/internal/domain"
)

type assetRepo struct {
	store *DB
}

const assetColumns = "id, project_id, filename, format, kind, declared_sum, checksum, bytes, duration_ms, status, reject_reason, retention_until, ingested_at, verified_at, created_at, updated_at"

func (r *assetRepo) Create(ctx context.Context, asset *domain.MediaAsset) error {
	if asset == nil {
		return domain.NewValidationError("asset", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO media_assets ("+assetColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		asset.ID, asset.ProjectID, asset.Filename, asset.Format, string(asset.Kind),
		asset.DeclaredSum, asset.Checksum, asset.Bytes, asset.DurationMS, string(asset.Status),
		asset.RejectReason, millis(asset.RetentionUntil), millis(asset.IngestedAt),
		nullableMillis(asset.VerifiedAt), millis(asset.CreatedAt), millis(asset.UpdatedAt),
	)
	return translate(err, "assets.create")
}

func (r *assetRepo) Update(ctx context.Context, asset *domain.MediaAsset) error {
	if asset == nil {
		return domain.NewValidationError("asset", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE media_assets SET filename = ?, format = ?, kind = ?, checksum = ?, bytes = ?,
		        duration_ms = ?, status = ?, reject_reason = ?, retention_until = ?,
		        verified_at = ?, updated_at = ?
		 WHERE id = ?`,
		asset.Filename, asset.Format, string(asset.Kind), asset.Checksum, asset.Bytes,
		asset.DurationMS, string(asset.Status), asset.RejectReason, millis(asset.RetentionUntil),
		nullableMillis(asset.VerifiedAt), millis(asset.UpdatedAt), asset.ID,
	)
	if err != nil {
		return translate(err, "assets.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("assets.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("assets.update: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *assetRepo) GetByID(ctx context.Context, id string) (*domain.MediaAsset, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+assetColumns+" FROM media_assets WHERE id = ?", id)
	return scanAsset(row)
}

func (r *assetRepo) GetByDeclaredChecksum(ctx context.Context, projectID, checksum string) (*domain.MediaAsset, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+assetColumns+" FROM media_assets WHERE project_id = ? AND declared_sum = ?",
		projectID, strings.ToLower(strings.TrimSpace(checksum)),
	)
	return scanAsset(row)
}

func (r *assetRepo) ListByIDs(ctx context.Context, projectID string, ids []string) ([]*domain.MediaAsset, error) {
	if len(ids) == 0 {
		return []*domain.MediaAsset{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, projectID)
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := r.store.conn(ctx).QueryContext(ctx,
		"SELECT "+assetColumns+" FROM media_assets WHERE project_id = ? AND id IN ("+strings.Join(placeholders, ", ")+")",
		args...,
	)
	if err != nil {
		return nil, translate(err, "assets.list_by_ids")
	}
	defer rows.Close()

	out := make([]*domain.MediaAsset, 0, len(ids))
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "assets.list_by_ids")
	}
	return out, nil
}

func (r *assetRepo) List(ctx context.Context, filter domain.AssetFilter, page domain.Page) (domain.PageResult[*domain.MediaAsset], error) {
	var empty domain.PageResult[*domain.MediaAsset]
	where, args := assetFilterClause(filter)
	conn := r.store.conn(ctx)

	var total int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_assets "+where, args...).Scan(&total); err != nil {
		return empty, translate(err, "assets.count")
	}
	order := orderClause(page, map[string]string{
		"filename":    "filename",
		"created_at":  "created_at",
		"duration_ms": "duration_ms",
		"bytes":       "bytes",
	}, "created_at")
	listArgs := append(append([]any{}, args...), page.Limit(), page.Offset())
	rows, err := conn.QueryContext(ctx,
		"SELECT "+assetColumns+" FROM media_assets "+where+" "+order+" LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return empty, translate(err, "assets.list")
	}
	defer rows.Close()

	items := make([]*domain.MediaAsset, 0, page.Limit())
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return empty, err
		}
		items = append(items, asset)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "assets.list")
	}
	return domain.NewPageResult(items, total, page), nil
}

func assetFilterClause(filter domain.AssetFilter) (string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if strings.TrimSpace(filter.ProjectID) != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, filter.ProjectID)
	}
	if strings.TrimSpace(string(filter.Status)) != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}
	if strings.TrimSpace(string(filter.Kind)) != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, string(filter.Kind))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		conditions = append(conditions, "filename LIKE ?")
		args = append(args, "%"+search+"%")
	}
	if len(conditions) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func scanAsset(row scannable) (*domain.MediaAsset, error) {
	var (
		asset      domain.MediaAsset
		kind       string
		status     string
		retention  int64
		ingestedAt int64
		verifiedAt sql.NullInt64
		createdAt  int64
		updatedAt  int64
	)
	if err := row.Scan(&asset.ID, &asset.ProjectID, &asset.Filename, &asset.Format, &kind,
		&asset.DeclaredSum, &asset.Checksum, &asset.Bytes, &asset.DurationMS, &status,
		&asset.RejectReason, &retention, &ingestedAt, &verifiedAt, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "assets.get")
	}
	asset.Kind = domain.AssetKind(kind)
	asset.Status = domain.AssetStatus(status)
	asset.RetentionUntil = fromMillis(retention)
	asset.IngestedAt = fromMillis(ingestedAt)
	asset.VerifiedAt = fromNullableMillis(verifiedAt)
	asset.CreatedAt = fromMillis(createdAt)
	asset.UpdatedAt = fromMillis(updatedAt)
	return &asset, nil
}
