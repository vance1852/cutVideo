package app_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/service/render"
)

// A submission the caller walked away from must leave nothing behind, and the
// same sealed cut must still be submittable afterwards.
func TestAbandonedRenderSubmissionLeavesNoQueuedWork(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REEL21")

	editor, err := h.app.Store.Users().GetByEmail(context.Background(), editorEmail)
	if err != nil {
		t.Fatalf("load the editor account: %v", err)
	}
	actor := domain.Principal{UserID: editor.ID, Role: domain.RoleEditor}

	abandoned, abandon := context.WithCancel(context.Background())
	abandon()
	if _, err := h.app.Render.Submit(abandoned, actor, render.SubmitInput{
		TimelineID:     timelineID,
		Preset:         "web_1080p",
		Priority:       domain.PriorityNormal,
		IdempotencyKey: "reel21-abandoned",
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("an abandoned submission must report the cancellation to the caller, got %v", err)
	}

	listed := h.call(http.MethodGet, "/api/v1/renders?project_id="+projectID, editorToken, nil, nil)
	if listed.status != http.StatusOK {
		t.Fatalf("listing renders failed: %d %v", listed.status, listed.body)
	}
	if total := listed.body["total"].(float64); total != 0 {
		t.Fatalf("an abandoned submission must not leave a render job behind, found %v: %v", total, listed.body)
	}

	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", editorToken, nil, nil)
	if capacity.body["queued_jobs"].(float64) != 0 || capacity.body["busy"].(float64) != 0 {
		t.Fatalf("abandoned work must not consume farm capacity: %v", capacity.body)
	}

	events := h.call(http.MethodGet, "/api/v1/audit-events?object_kind=render_job&page_size=50", supervisorToken, nil, nil)
	if events.status != http.StatusOK {
		t.Fatalf("listing audit events failed: %d %v", events.status, events.body)
	}
	if events.body["total"].(float64) != 0 {
		t.Fatalf("no render audit event may survive an abandoned submission: %v", events.body)
	}

	resubmitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "web_1080p",
		"priority":    50,
	}, nil)
	if resubmitted.status != http.StatusAccepted {
		t.Fatalf("re-submitting the same sealed cut must be accepted: %d %v", resubmitted.status, resubmitted.body)
	}
	jobID := h.stringField(resubmitted, "id")

	conflict := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "web_1080p",
		"priority":    50,
	}, nil)
	if conflict.status != http.StatusConflict {
		t.Fatalf("a second active render for the same cut must still conflict: %d %v", conflict.status, conflict.body)
	}

	claimed, err := h.app.Render.Claim(context.Background(), "primary")
	if err != nil {
		t.Fatalf("claiming the re-submitted render failed: %v", err)
	}
	if claimed == nil || claimed.ID != jobID {
		t.Fatalf("the re-submitted render must reach the farm, claimed %+v", claimed)
	}
}
