// Package idempotency replays the first response produced for a mutating request
// instead of performing the work twice. The stored key is scoped by request
// method, path and actor so an unrelated caller cannot collide with it.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/repository"
)

// ErrPayloadMismatch is returned when a key is replayed with a different body.
var ErrPayloadMismatch = errors.New("idempotency: key was already used with a different payload")

// Request identifies one mutating call for idempotency purposes.
type Request struct {
	Scope   string
	Method  string
	Path    string
	ActorID string
	Key     string
	Payload string
}

// Fingerprint derives the stable hash of the request payload.
func (r Request) Fingerprint() string {
	return ids.Fingerprint(r.Scope, strings.ToUpper(r.Method), r.Path, r.ActorID, r.Payload)
}

// Present reports whether the caller supplied a key.
func (r Request) Present() bool { return strings.TrimSpace(r.Key) != "" }

// Guard stores and replays idempotent responses.
type Guard struct {
	repo repository.IdempotencyRepository
	gen  ids.Generator
	clk  clock.Clock
	ttl  time.Duration
}

// NewGuard builds a guard with the configured retention window.
func NewGuard(repo repository.IdempotencyRepository, gen ids.Generator, clk clock.Clock, ttl time.Duration) *Guard {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Guard{repo: repo, gen: gen, clk: clk, ttl: ttl}
}

// Lookup returns a stored response for the request, if one is still valid.
func (g *Guard) Lookup(ctx context.Context, request Request) (*domain.IdempotencyRecord, error) {
	if !request.Present() {
		return nil, nil
	}
	record, err := g.repo.Get(ctx, request.Scope, request.Method, request.Path, request.ActorID, request.Key)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if record.Expired(g.clk.Now()) {
		return nil, nil
	}
	if !record.Matches(request.Fingerprint()) {
		return nil, fmt.Errorf("%w: scope %s key %s", ErrPayloadMismatch, request.Scope, request.Key)
	}
	return record, nil
}

// Remember stores the response for later replay.
func (g *Guard) Remember(ctx context.Context, request Request, status int, body string) error {
	if !request.Present() {
		return nil
	}
	record, err := domain.NewIdempotencyRecord(
		g.gen.NewID("idem"),
		request.Scope,
		request.Method,
		request.Path,
		request.ActorID,
		request.Key,
		request.Fingerprint(),
		status,
		body,
		g.clk.Now(),
		g.ttl,
	)
	if err != nil {
		return err
	}
	if err := g.repo.Put(ctx, record); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil
		}
		return err
	}
	return nil
}

// Purge removes expired records and reports how many were dropped.
func (g *Guard) Purge(ctx context.Context) (int, error) {
	return g.repo.DeleteExpired(ctx, g.clk.Now())
}
