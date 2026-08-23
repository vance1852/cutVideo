package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
)

// Probe reports whether a dependency is usable.
type Probe interface {
	Ping(ctx context.Context) error
}

type healthResponse struct {
	Status    string            `json:"status"`
	Version   string            `json:"version"`
	Checks    map[string]string `json:"checks"`
	CheckedAt string            `json:"checked_at"`
}

func (rt *Router) handleLiveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, healthResponse{
		Status:    "alive",
		Version:   rt.version,
		Checks:    map[string]string{"process": "ok"},
		CheckedAt: formatTime(rt.clk.Now()),
	})
}

// handleReadiness verifies the dependencies a request actually needs: the
// relational store must answer, and the render farm must have at least one seat
// that is not offline.
func (rt *Router) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{}
	ready := true

	if err := rt.store.Ping(ctx); err != nil {
		checks["database"] = "unavailable"
		ready = false
	} else {
		checks["database"] = "ok"
	}

	capacity, err := rt.render.Capacity(ctx, "")
	switch {
	case err != nil:
		checks["render_farm"] = "unknown"
		ready = false
	case capacity.Total == 0:
		checks["render_farm"] = "no seats provisioned"
		ready = false
	case capacity.Idle == 0 && capacity.Busy == 0:
		checks["render_farm"] = "all seats offline"
		ready = false
	default:
		checks["render_farm"] = "ok"
	}

	response := healthResponse{
		Status:    "ready",
		Version:   rt.version,
		Checks:    checks,
		CheckedAt: formatTime(rt.clk.Now()),
	}
	if !ready {
		response.Status = "degraded"
		writeJSON(w, r, http.StatusServiceUnavailable, response)
		return
	}
	writeJSON(w, r, http.StatusOK, response)
}

func (rt *Router) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, apierr.New(apierr.CodeNotFound, "no route matches this request"))
}
