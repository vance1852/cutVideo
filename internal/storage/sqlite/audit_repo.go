package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

type auditRepo struct {
	store *DB
}

const auditColumns = "id, actor_id, actor_role, action, object_kind, object_id, result, request_id, detail, created_at"

func (r *auditRepo) Append(ctx context.Context, event *domain.AuditEvent) error {
	if event == nil {
		return domain.NewValidationError("audit_event", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO audit_events ("+auditColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		event.ID, event.ActorID, string(event.ActorRole), event.Action, event.ObjectKind,
		event.ObjectID, string(event.Result), event.RequestID, event.Detail, millis(event.CreatedAt),
	)
	return translate(err, "audits.append")
}

func (r *auditRepo) List(ctx context.Context, filter domain.AuditFilter, page domain.Page) (domain.PageResult[*domain.AuditEvent], error) {
	var empty domain.PageResult[*domain.AuditEvent]
	where, args := auditFilterClause(filter)
	conn := r.store.conn(ctx)

	var total int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events "+where, args...).Scan(&total); err != nil {
		return empty, translate(err, "audits.count")
	}
	order := orderClause(page, map[string]string{
		"created_at": "created_at",
		"action":     "action",
	}, "created_at")
	listArgs := append(append([]any{}, args...), page.Limit(), page.Offset())
	rows, err := conn.QueryContext(ctx,
		"SELECT "+auditColumns+" FROM audit_events "+where+" "+order+" LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return empty, translate(err, "audits.list")
	}
	defer rows.Close()

	items := make([]*domain.AuditEvent, 0, page.Limit())
	for rows.Next() {
		var (
			event     domain.AuditEvent
			role      string
			result    string
			createdAt int64
		)
		if err := rows.Scan(&event.ID, &event.ActorID, &role, &event.Action, &event.ObjectKind,
			&event.ObjectID, &result, &event.RequestID, &event.Detail, &createdAt); err != nil {
			return empty, translate(err, "audits.scan")
		}
		event.ActorRole = domain.Role(role)
		event.Result = domain.AuditResult(result)
		event.CreatedAt = fromMillis(createdAt)
		items = append(items, &event)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "audits.list")
	}
	return domain.NewPageResult(items, total, page), nil
}

func auditFilterClause(filter domain.AuditFilter) (string, []any) {
	conditions := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if strings.TrimSpace(filter.ActorID) != "" {
		conditions = append(conditions, "actor_id = ?")
		args = append(args, filter.ActorID)
	}
	if strings.TrimSpace(filter.Action) != "" {
		conditions = append(conditions, "action = ?")
		args = append(args, filter.Action)
	}
	if strings.TrimSpace(filter.ObjectKind) != "" {
		conditions = append(conditions, "object_kind = ?")
		args = append(args, filter.ObjectKind)
	}
	if strings.TrimSpace(filter.ObjectID) != "" {
		conditions = append(conditions, "object_id = ?")
		args = append(args, filter.ObjectID)
	}
	if strings.TrimSpace(string(filter.Result)) != "" {
		conditions = append(conditions, "result = ?")
		args = append(args, string(filter.Result))
	}
	if filter.Since != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, millis(*filter.Since))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

type idempotencyRepo struct {
	store *DB
}

const idempotencyColumns = "id, scope, method, path, actor_id, key, request_hash, response_code, response_body, created_at, expires_at"

func (r *idempotencyRepo) Get(ctx context.Context, scope, method, path, actorID, key string) (*domain.IdempotencyRecord, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT "+idempotencyColumns+` FROM idempotency_keys
		 WHERE scope = ? AND method = ? AND path = ? AND actor_id = ? AND key = ?`,
		scope, strings.ToUpper(strings.TrimSpace(method)), path, actorID, strings.TrimSpace(key),
	)
	var (
		record    domain.IdempotencyRecord
		createdAt int64
		expiresAt int64
	)
	if err := row.Scan(&record.ID, &record.Scope, &record.Method, &record.Path, &record.ActorID,
		&record.Key, &record.RequestHash, &record.ResponseCode, &record.ResponseBody,
		&createdAt, &expiresAt); err != nil {
		return nil, translate(err, "idempotency.get")
	}
	record.CreatedAt = fromMillis(createdAt)
	record.ExpiresAt = fromMillis(expiresAt)
	return &record, nil
}

func (r *idempotencyRepo) Put(ctx context.Context, record *domain.IdempotencyRecord) error {
	if record == nil {
		return domain.NewValidationError("idempotency_record", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO idempotency_keys ("+idempotencyColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		record.ID, record.Scope, record.Method, record.Path, record.ActorID, record.Key,
		record.RequestHash, record.ResponseCode, record.ResponseBody,
		millis(record.CreatedAt), millis(record.ExpiresAt),
	)
	return translate(err, "idempotency.put")
}

func (r *idempotencyRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	result, err := r.store.conn(ctx).ExecContext(ctx,
		"DELETE FROM idempotency_keys WHERE expires_at < ?", millis(before))
	if err != nil {
		return 0, translate(err, "idempotency.delete_expired")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("idempotency.delete_expired: rows affected: %w", err)
	}
	return int(affected), nil
}
