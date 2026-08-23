package sqlite

import (
	"context"
	"fmt"

	"github.com/vance1852/cutVideo/internal/domain"
)

type userRepo struct {
	store *DB
}

const userColumns = "id, email, display_name, role, status, password_hash, created_at, updated_at"

func (r *userRepo) Create(ctx context.Context, user *domain.User) error {
	if user == nil {
		return domain.NewValidationError("user", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO users ("+userColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		user.ID, user.Email, user.DisplayName, string(user.Role), string(user.Status),
		user.PasswordHash, millis(user.CreatedAt), millis(user.UpdatedAt),
	)
	return translate(err, "users.create")
}

func (r *userRepo) Update(ctx context.Context, user *domain.User) error {
	if user == nil {
		return domain.NewValidationError("user", "must not be nil")
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE users SET email = ?, display_name = ?, role = ?, status = ?, password_hash = ?, updated_at = ?
		 WHERE id = ?`,
		user.Email, user.DisplayName, string(user.Role), string(user.Status),
		user.PasswordHash, millis(user.UpdatedAt), user.ID,
	)
	if err != nil {
		return translate(err, "users.update")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("users.update: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("users.update: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *userRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id)
	return scanUser(row)
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	normalized, err := domain.NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE email = ?", normalized)
	return scanUser(row)
}

func (r *userRepo) List(ctx context.Context, page domain.Page) (domain.PageResult[*domain.User], error) {
	var empty domain.PageResult[*domain.User]
	conn := r.store.conn(ctx)

	var total int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&total); err != nil {
		return empty, translate(err, "users.count")
	}
	order := orderClause(page, map[string]string{
		"email":      "email",
		"created_at": "created_at",
		"role":       "role",
	}, "created_at")
	rows, err := conn.QueryContext(ctx,
		"SELECT "+userColumns+" FROM users "+order+" LIMIT ? OFFSET ?",
		page.Limit(), page.Offset(),
	)
	if err != nil {
		return empty, translate(err, "users.list")
	}
	defer rows.Close()

	items := make([]*domain.User, 0, page.Limit())
	for rows.Next() {
		user, err := scanUserRows(rows)
		if err != nil {
			return empty, err
		}
		items = append(items, user)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "users.list")
	}
	return domain.NewPageResult(items, total, page), nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanUser(row scannable) (*domain.User, error) {
	user, err := scanUserRows(row)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func scanUserRows(row scannable) (*domain.User, error) {
	var (
		user      domain.User
		role      string
		status    string
		createdAt int64
		updatedAt int64
	)
	if err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &role, &status,
		&user.PasswordHash, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "users.get")
	}
	user.Role = domain.Role(role)
	user.Status = domain.UserStatus(status)
	user.CreatedAt = fromMillis(createdAt)
	user.UpdatedAt = fromMillis(updatedAt)
	return &user, nil
}
