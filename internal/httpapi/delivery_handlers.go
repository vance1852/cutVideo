package httpapi

import (
	"net/http"

	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/middleware"
	"github.com/vance1852/cutVideo/internal/service/delivery"
)

type createTargetRequest struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Endpoint      string `json:"endpoint"`
	CredentialRef string `json:"credential_ref"`
}

func (rt *Router) handleCreateTarget(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body createTargetRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	target, err := rt.delivery.CreateTarget(r.Context(), middleware.PrincipalFrom(r.Context()), delivery.CreateTargetInput{
		ProjectID:     projectID,
		Name:          body.Name,
		Kind:          domain.DeliveryKind(body.Kind),
		Endpoint:      body.Endpoint,
		CredentialRef: body.CredentialRef,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, newTargetView(target))
}

func (rt *Router) handleListTargets(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	onlyEnabled := r.URL.Query().Get("enabled") == "true"
	targets, err := rt.delivery.ListTargets(r.Context(), middleware.PrincipalFrom(r.Context()), projectID, onlyEnabled)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views := make([]targetView, 0, len(targets))
	for _, target := range targets {
		views = append(views, newTargetView(target))
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"items": views, "total": len(views)})
}

type setTargetStateRequest struct {
	Enabled bool `json:"enabled"`
}

func (rt *Router) handleSetTargetState(w http.ResponseWriter, r *http.Request) {
	targetID, err := pathValue(r, "targetID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body setTargetStateRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	target, err := rt.delivery.SetTargetEnabled(r.Context(), middleware.PrincipalFrom(r.Context()), targetID, body.Enabled)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newTargetView(target))
}

func (rt *Router) handleDispatchDeliveries(w http.ResponseWriter, r *http.Request) {
	jobID, err := pathValue(r, "jobID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := rt.delivery.Dispatch(r.Context(), middleware.PrincipalFrom(r.Context()), jobID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	outcomes := make([]map[string]any, 0, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		entry := map[string]any{
			"target_id":   outcome.TargetID,
			"target_name": outcome.TargetName,
			"status":      string(outcome.Status),
		}
		if outcome.Failure != "" {
			entry["failure"] = outcome.Failure
		}
		outcomes = append(outcomes, entry)
	}
	status := http.StatusOK
	if result.Failed > 0 && result.Confirmed > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, r, status, map[string]any{
		"job_id":    result.JobID,
		"requested": result.Requested,
		"confirmed": result.Confirmed,
		"failed":    result.Failed,
		"skipped":   result.Skipped,
		"outcomes":  outcomes,
	})
}

func (rt *Router) handleListDeliveryRecords(w http.ResponseWriter, r *http.Request) {
	jobID, err := pathValue(r, "jobID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	records, err := rt.delivery.ListRecords(r.Context(), middleware.PrincipalFrom(r.Context()), jobID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views := make([]deliveryRecordView, 0, len(records))
	for _, record := range records {
		views = append(views, newDeliveryRecordView(record))
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"items": views, "total": len(views)})
}
