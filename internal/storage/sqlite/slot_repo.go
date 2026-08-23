package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

type slotRepo struct {
	store *DB
}

const slotColumns = "id, name, pool, units, status, held_by_job_id, leased_at, released_at, created_at, updated_at"

func (r *slotRepo) Create(ctx context.Context, slot *domain.RenderSlot) error {
	if slot == nil {
		return domain.NewValidationError("render_slot", "must not be nil")
	}
	_, err := r.store.conn(ctx).ExecContext(ctx,
		"INSERT INTO render_slots ("+slotColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		slot.ID, slot.Name, slot.Pool, slot.Units, string(slot.Status), slot.HeldByJobID,
		nullableMillis(slot.LeasedAt), nullableMillis(slot.ReleasedAt),
		millis(slot.CreatedAt), millis(slot.UpdatedAt),
	)
	return translate(err, "slots.create")
}

func (r *slotRepo) GetByID(ctx context.Context, id string) (*domain.RenderSlot, error) {
	row := r.store.conn(ctx).QueryRowContext(ctx, "SELECT "+slotColumns+" FROM render_slots WHERE id = ?", id)
	return scanSlot(row)
}

// ReserveIdle claims one idle slot for the job with a single conditional update.
// Two concurrent claims cannot both win because the WHERE clause only matches a
// row that is still idle and unheld.
func (r *slotRepo) ReserveIdle(ctx context.Context, pool, jobID string, now time.Time) (*domain.RenderSlot, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, domain.NewValidationError("job_id", "must not be empty")
	}
	conn := r.store.conn(ctx)
	for attempt := 0; attempt < 8; attempt++ {
		var candidate string
		query := `SELECT id FROM render_slots WHERE status = ? AND held_by_job_id = ''`
		args := []any{string(domain.SlotIdle)}
		if strings.TrimSpace(pool) != "" {
			query += " AND pool = ?"
			args = append(args, strings.TrimSpace(pool))
		}
		query += " ORDER BY units DESC, name ASC LIMIT 1"
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&candidate); err != nil {
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("slots.reserve: %w", domain.ErrCapacityExhausted)
			}
			return nil, translate(err, "slots.reserve")
		}
		result, err := conn.ExecContext(ctx,
			`UPDATE render_slots SET status = ?, held_by_job_id = ?, leased_at = ?, released_at = NULL, updated_at = ?
			 WHERE id = ? AND status = ? AND held_by_job_id = ''`,
			string(domain.SlotBusy), strings.TrimSpace(jobID), millis(now), millis(now),
			candidate, string(domain.SlotIdle),
		)
		if err != nil {
			return nil, translate(err, "slots.reserve")
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("slots.reserve: rows affected: %w", err)
		}
		if affected == 1 {
			return r.GetByID(ctx, candidate)
		}
	}
	return nil, fmt.Errorf("slots.reserve: %w", domain.ErrCapacityExhausted)
}

// Release returns a slot to the pool. A draining slot goes offline so it stops
// receiving new work.
func (r *slotRepo) Release(ctx context.Context, slotID, jobID string, now time.Time) error {
	slot, err := r.GetByID(ctx, slotID)
	if err != nil {
		return err
	}
	if slot.Status == domain.SlotDraining {
		return r.markOffline(ctx, slotID, now)
	}
	if err := slot.Release(jobID, now); err != nil {
		return err
	}
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE render_slots SET status = ?, held_by_job_id = '', leased_at = NULL, released_at = ?, updated_at = ?
		 WHERE id = ? AND held_by_job_id = ?`,
		string(domain.SlotIdle), millis(now), millis(now), slotID, strings.TrimSpace(jobID),
	)
	if err != nil {
		return translate(err, "slots.release")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("slots.release: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("slots.release %s: %w", slotID, domain.ErrConflict)
	}
	return nil
}

// ForceRelease frees a slot regardless of its current holder. It is used by the
// lease reaper when a worker disappeared without releasing its seat.
func (r *slotRepo) ForceRelease(ctx context.Context, slotID string, now time.Time) error {
	result, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE render_slots SET status = CASE WHEN status = ? THEN ? ELSE ? END,
		        held_by_job_id = '', leased_at = NULL, released_at = ?, updated_at = ?
		 WHERE id = ?`,
		string(domain.SlotDraining), string(domain.SlotOffline), string(domain.SlotIdle),
		millis(now), millis(now), slotID,
	)
	if err != nil {
		return translate(err, "slots.force_release")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("slots.force_release: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("slots.force_release: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *slotRepo) markOffline(ctx context.Context, slotID string, now time.Time) error {
	_, err := r.store.conn(ctx).ExecContext(ctx,
		`UPDATE render_slots SET status = ?, held_by_job_id = '', leased_at = NULL, released_at = ?, updated_at = ?
		 WHERE id = ?`,
		string(domain.SlotOffline), millis(now), millis(now), slotID,
	)
	return translate(err, "slots.drain_release")
}

func (r *slotRepo) List(ctx context.Context, pool string) ([]*domain.RenderSlot, error) {
	query := "SELECT " + slotColumns + " FROM render_slots"
	args := []any{}
	if strings.TrimSpace(pool) != "" {
		query += " WHERE pool = ?"
		args = append(args, strings.TrimSpace(pool))
	}
	query += " ORDER BY name ASC"
	rows, err := r.store.conn(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, translate(err, "slots.list")
	}
	defer rows.Close()

	out := make([]*domain.RenderSlot, 0, 8)
	for rows.Next() {
		slot, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, slot)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "slots.list")
	}
	return out, nil
}

func (r *slotRepo) Capacity(ctx context.Context, pool string) (domain.FarmCapacity, error) {
	slots, err := r.List(ctx, pool)
	if err != nil {
		return domain.FarmCapacity{}, err
	}
	capacity := domain.FarmCapacity{Pool: pool, Total: len(slots)}
	for _, slot := range slots {
		switch slot.Status {
		case domain.SlotIdle:
			capacity.Idle++
		case domain.SlotBusy:
			capacity.Busy++
		case domain.SlotDraining:
			capacity.Draining++
		case domain.SlotOffline:
			capacity.Offline++
		}
	}
	var queued int
	if err := r.store.conn(ctx).QueryRowContext(ctx,
		"SELECT COUNT(*) FROM render_jobs WHERE status = ?", string(domain.RenderQueued),
	).Scan(&queued); err != nil {
		return domain.FarmCapacity{}, translate(err, "slots.capacity")
	}
	capacity.QueuedJob = queued
	return capacity, nil
}

func scanSlot(row scannable) (*domain.RenderSlot, error) {
	var (
		slot       domain.RenderSlot
		status     string
		leasedAt   sql.NullInt64
		releasedAt sql.NullInt64
		createdAt  int64
		updatedAt  int64
	)
	if err := row.Scan(&slot.ID, &slot.Name, &slot.Pool, &slot.Units, &status,
		&slot.HeldByJobID, &leasedAt, &releasedAt, &createdAt, &updatedAt); err != nil {
		return nil, translate(err, "slots.get")
	}
	slot.Status = domain.SlotStatus(status)
	slot.LeasedAt = fromNullableMillis(leasedAt)
	slot.ReleasedAt = fromNullableMillis(releasedAt)
	slot.CreatedAt = fromMillis(createdAt)
	slot.UpdatedAt = fromMillis(updatedAt)
	return &slot, nil
}
