package httpapi

import (
	"net/http"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/middleware"
	"github.com/vance1852/cutVideo/internal/service/editing"
)

type createProjectRequest struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	FrameRate  int    `json:"frame_rate"`
	Resolution string `json:"resolution"`
	DeadlineAt string `json:"deadline_at"`
}

func (rt *Router) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body createProjectRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	deadline, err := time.Parse(time.RFC3339, body.DeadlineAt)
	if err != nil {
		writeError(w, r, apierr.Wrap(apierr.CodeInvalidRequest, "deadline_at must be an RFC3339 timestamp", err))
		return
	}
	project, err := rt.editing.CreateProject(r.Context(), middleware.PrincipalFrom(r.Context()), editing.CreateProjectInput{
		Code:       body.Code,
		Title:      body.Title,
		FrameRate:  body.FrameRate,
		Resolution: body.Resolution,
		Deadline:   deadline,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, newProjectView(project))
}

func (rt *Router) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	project, err := rt.editing.GetProject(r.Context(), middleware.PrincipalFrom(r.Context()), projectID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newProjectView(project))
}

func (rt *Router) handleListProjects(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	filter := domain.ProjectFilter{
		Status: domain.ProjectStatus(query.Get("status")),
		Search: query.Get("q"),
	}
	if owner := query.Get("owner_id"); owner != "" {
		filter.OwnerID = owner
	}
	result, err := rt.editing.ListProjects(r.Context(), middleware.PrincipalFrom(r.Context()), filter, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views := make([]projectView, 0, len(result.Items))
	for _, project := range result.Items {
		views = append(views, newProjectView(project))
	}
	writeJSON(w, r, http.StatusOK, pageEnvelope{
		Items:      views,
		Total:      result.Total,
		Page:       result.Page,
		PageSize:   result.Size,
		TotalPages: result.TotalPages,
	})
}

func (rt *Router) handleLockProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	project, err := rt.editing.LockProject(r.Context(), middleware.PrincipalFrom(r.Context()), projectID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newProjectView(project))
}

func (rt *Router) handleUnlockProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	project, err := rt.editing.UnlockProject(r.Context(), middleware.PrincipalFrom(r.Context()), projectID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newProjectView(project))
}

type createDraftRequest struct {
	Notes string `json:"notes"`
}

func (rt *Router) handleCreateDraft(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body createDraftRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, r, err)
			return
		}
	}
	version, err := rt.editing.CreateDraft(r.Context(), middleware.PrincipalFrom(r.Context()), projectID, body.Notes)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, newTimelineView(version))
}

func (rt *Router) handleListTimelines(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := parsePage(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	filter := domain.TimelineFilter{
		ProjectID: projectID,
		Status:    domain.TimelineStatus(r.URL.Query().Get("status")),
	}
	result, err := rt.editing.ListTimelines(r.Context(), middleware.PrincipalFrom(r.Context()), filter, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views := make([]timelineView, 0, len(result.Items))
	for _, version := range result.Items {
		views = append(views, newTimelineView(version))
	}
	writeJSON(w, r, http.StatusOK, pageEnvelope{
		Items:      views,
		Total:      result.Total,
		Page:       result.Page,
		PageSize:   result.Size,
		TotalPages: result.TotalPages,
	})
}

func (rt *Router) handleGetTimeline(w http.ResponseWriter, r *http.Request) {
	timelineID, err := pathValue(r, "timelineID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	version, err := rt.editing.GetTimeline(r.Context(), middleware.PrincipalFrom(r.Context()), timelineID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newTimelineView(version))
}

type addClipRequest struct {
	AssetID      string `json:"asset_id"`
	OrderIndex   int    `json:"order_index"`
	SourceInMS   int64  `json:"source_in_ms"`
	SourceOutMS  int64  `json:"source_out_ms"`
	Track        string `json:"track"`
	Transition   string `json:"transition"`
	SpeedPercent int    `json:"speed_percent"`
}

func (rt *Router) handleAddClip(w http.ResponseWriter, r *http.Request) {
	timelineID, err := pathValue(r, "timelineID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body addClipRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := rt.editing.AddClip(r.Context(), middleware.PrincipalFrom(r.Context()), editing.AddClipInput{
		TimelineID:   timelineID,
		AssetID:      body.AssetID,
		OrderIndex:   body.OrderIndex,
		SourceInMS:   body.SourceInMS,
		SourceOutMS:  body.SourceOutMS,
		Track:        domain.Track(body.Track),
		Transition:   body.Transition,
		SpeedPercent: body.SpeedPercent,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, newTimelineView(version))
}

func (rt *Router) handleRemoveClip(w http.ResponseWriter, r *http.Request) {
	timelineID, err := pathValue(r, "timelineID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	clipID, err := pathValue(r, "clipID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	version, err := rt.editing.RemoveClip(r.Context(), middleware.PrincipalFrom(r.Context()), timelineID, clipID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newTimelineView(version))
}

func (rt *Router) handleSealTimeline(w http.ResponseWriter, r *http.Request) {
	timelineID, err := pathValue(r, "timelineID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	version, err := rt.editing.Seal(r.Context(), middleware.PrincipalFrom(r.Context()), timelineID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newTimelineView(version))
}
