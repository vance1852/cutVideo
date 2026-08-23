package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/vance1852/cutVideo/internal/domain"
)

type projectRepo struct {
	store *DB
}

const projectColumns = "id, code, title, owner_id, status, frame_rate, resolution, current_version, sealed_version, deadline_at, created_at, updated_at"

func (r *projectRepo) Create(ctx context.Context, project *domain.Project) error {
	if project == nil {
		return domain.NewValidationError("project", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO projects ("+projectColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		project.ID, project.Code, project.Title, project.OwnerID, string(project.Status),
		project.FrameRate, project.Resolution, project.CurrentVersion, project.SealedVersion,
		millis(project.DeadlineAt), millis(project.CreatedAt), millis(project.UpdatedAt),
	)
	return translate(err, "projects.create")
}

func (r *projectRepo) Update(ctx context.Context, project *domain.Project) error {
	if project == nil {
		return domain.NewValidationError("project", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE projects SET title = ?, status = ?, frame_rate = ?, resolution = ?,
		        current_version = ?, sealed_version = ?, deadline_at = ?, updated_at = ?
		 WHERE id = ?`,
		project.Title, string(project.Status), project.FrameRate, project.Resolution,
		project.CurrentVersion, project.SealedVersion, millis(project.DeadlineAt),
		millis(project.UpdatedAt), project.ID,
	)
	if err != nil {
		return translate(err, "projects.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("projects.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("projects.update: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *projectRepo) GetByID(ctx context.Context, id string) (*domain.Project, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = ?", id)
	return scanProject(row)
}

func (r *projectRepo) GetByCode(ctx context.Context, code string) (*domain.Project, error) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE code = ?", normalized)
	return scanProject(row)
}

func (r *projectRepo) List(ctx context.Context, filter domain.ProjectFilter, page domain.Page) (domain.PageResult[*domain.Project], error) {
	var empty domain.PageResult[*domain.Project]
	where, args := projectFilterClause(filter)
	conn := r.store.conn(ctx)

	var total int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects "+where, args...).Scan(&total); err != nil {
		return empty, translate(err, "projects.count")
	}
	order := orderClause(page, map[string]string{
		"code":        "code",
		"created_at":  "created_at",
		"deadline_at": "deadline_at",
		"status":      "status",
	}, "created_at")
	listArgs := append(append([]any{}, args...), page.Limit(), page.Offset())
	rows, err := conn.QueryContext(ctx,
		"SELECT "+projectColumns+" FROM projects "+where+" "+order+" LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return empty, translate(err, "projects.list")
	}
	defer rows.Close()

	items := make([]*domain.Project, 0, page.Limit())
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return empty, err
		}
		items = append(items, project)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "projects.list")
	}
	return domain.NewPageResult(items, total, page), nil
}

// projectFilterClause builds the shared WHERE fragment. The same fragment backs
// both the count and the page query so totals never disagree with items.
func projectFilterClause(filter domain.ProjectFilter) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if strings.TrimSpace(filter.OwnerID) != "" {
		conditions = append(conditions, "owner_id = ?")
		args = append(args, filter.OwnerID)
	}
	if strings.TrimSpace(string(filter.Status)) != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		conditions = append(conditions, "(title LIKE ? OR code LIKE ?)")
		pattern := "%" + search + "%"
		args = append(args, pattern, pattern)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func scanProject(row scannable) (*domain.Project, error) {
	var (
		project    domain.Project
		status     string
		deadlineAt int64
		createdAt  int64
		updatedAt  int64
	)
	if err := row.Scan(&project.ID, &project.Code, &project.Title, &project.OwnerID, &status,
		&project.FrameRate, &project.Resolution, &project.CurrentVersion, &project.SealedVersion,
		&deadlineAt, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "projects.get")
	}
	project.Status = domain.ProjectStatus(status)
	project.DeadlineAt = fromMillis(deadlineAt)
	project.CreatedAt = fromMillis(createdAt)
	project.UpdatedAt = fromMillis(updatedAt)
	return &project, nil
}
