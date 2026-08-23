package httpapi

import (
	"net/http"

	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/middleware"
	"github.com/vance1852/cutVideo/internal/service/render"
)

type submitRenderRequest struct {
	TimelineID string `json:"timeline_id"`
	Preset     string `json:"preset"`
	Priority   int    `json:"priority"`
}

func (rt *Router) handleSubmitRender(w http.ResponseWriter, r *http.Request) {
	var body submitRenderRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	job, err := rt.render.Submit(r.Context(), middleware.PrincipalFrom(r.Context()), render.SubmitInput{
		TimelineID:     body.TimelineID,
		Preset:         body.Preset,
		Priority:       domain.RenderPriority(body.Priority),
		IdempotencyKey: idempotencyKey(r),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusAccepted, newRenderView(job))
}

func (rt *Router) handleGetRender(w http.ResponseWriter, r *http.Request) {
	jobID, err := pathValue(r, "jobID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	job, err := rt.render.Get(r.Context(), middleware.PrincipalFrom(r.Context()), jobID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newRenderView(job))
}

func (rt *Router) handleListRenders(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	filter := domain.RenderFilter{
		ProjectID:  query.Get("project_id"),
		TimelineID: query.Get("timeline_id"),
		Status:     domain.RenderStatus(query.Get("status")),
		Preset:     query.Get("preset"),
		OnlyActive: query.Get("active") == "true",
	}
	result, err := rt.render.List(r.Context(), middleware.PrincipalFrom(r.Context()), filter, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views := make([]renderView, 0, len(result.Items))
	for _, job := range result.Items {
		views = append(views, newRenderView(job))
	}
	writeJSON(w, r, http.StatusOK, pageEnvelope{
		Items:      views,
		Total:      result.Total,
		Page:       result.Page,
		PageSize:   result.Size,
		TotalPages: result.TotalPages,
	})
}

type cancelRenderRequest struct {
	Reason string `json:"reason"`
}

func (rt *Router) handleCancelRender(w http.ResponseWriter, r *http.Request) {
	jobID, err := pathValue(r, "jobID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body cancelRenderRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, r, err)
			return
		}
	}
	job, err := rt.render.Cancel(r.Context(), middleware.PrincipalFrom(r.Context()), jobID, body.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newRenderView(job))
}

type provisionSlotRequest struct {
	Name  string `json:"name"`
	Pool  string `json:"pool"`
	Units int    `json:"units"`
}

func (rt *Router) handleProvisionSlot(w http.ResponseWriter, r *http.Request) {
	var body provisionSlotRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	slot, err := rt.render.ProvisionSlot(r.Context(), middleware.PrincipalFrom(r.Context()), body.Name, body.Pool, body.Units)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, slotView{
		ID:     slot.ID,
		Name:   slot.Name,
		Pool:   slot.Pool,
		Units:  slot.Units,
		Status: string(slot.Status),
	})
}

func (rt *Router) handleFarmCapacity(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	capacity, err := rt.render.Capacity(r.Context(), pool)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"pool":         capacity.Pool,
		"total":        capacity.Total,
		"idle":         capacity.Idle,
		"busy":         capacity.Busy,
		"draining":     capacity.Draining,
		"offline":      capacity.Offline,
		"queued_jobs":  capacity.QueuedJob,
		"saturated":    capacity.Saturated(),
	})
}
