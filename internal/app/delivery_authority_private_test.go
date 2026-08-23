package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
)

// Only a supervisor defines where masters go; an editor may look and distribute.
func TestOnlySupervisorsManageDeliveryDestinations(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REELB1")

	approved := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", supervisorToken, map[string]any{
		"name":           "broadcast",
		"kind":           "webhook",
		"endpoint":       "https://broadcast.invalid/ingest",
		"credential_ref": "vault://broadcast",
	}, nil)
	if approved.status != http.StatusCreated {
		t.Fatalf("the supervisor must be able to create a destination: %d %v", approved.status, approved.body)
	}
	approvedID := h.stringField(approved, "id")

	sneaked := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", editorToken, map[string]any{
		"name":           "personal-drop",
		"kind":           "s3",
		"endpoint":       "https://personal.invalid/drop",
		"credential_ref": "vault://personal",
	}, nil)
	if sneaked.status != http.StatusForbidden {
		t.Fatalf("an editor must not create destinations, got %d %v", sneaked.status, sneaked.body)
	}

	switchedOff := h.call(http.MethodPatch, "/api/v1/delivery-targets/"+approvedID, editorToken, map[string]any{
		"enabled": false,
	}, nil)
	if switchedOff.status != http.StatusForbidden {
		t.Fatalf("an editor must not switch destinations off, got %d %v", switchedOff.status, switchedOff.body)
	}

	listed := h.call(http.MethodGet, "/api/v1/projects/"+projectID+"/delivery-targets", editorToken, nil, nil)
	if listed.status != http.StatusOK {
		t.Fatalf("an editor must still be able to read the destinations: %d %v", listed.status, listed.body)
	}
	items := listed.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("only the approved destination may exist: %v", listed.body)
	}
	if items[0].(map[string]any)["status"] != string(domain.TargetEnabled) {
		t.Fatalf("the approved destination must still be enabled: %v", listed.body)
	}

	disabled := h.call(http.MethodPatch, "/api/v1/delivery-targets/"+approvedID, supervisorToken, map[string]any{
		"enabled": false,
	}, nil)
	if disabled.status != http.StatusOK || disabled.body["status"] != string(domain.TargetDisabled) {
		t.Fatalf("the supervisor must be able to disable a destination: %d %v", disabled.status, disabled.body)
	}
	reenabled := h.call(http.MethodPatch, "/api/v1/delivery-targets/"+approvedID, supervisorToken, map[string]any{
		"enabled": true,
	}, nil)
	if reenabled.status != http.StatusOK || reenabled.body["status"] != string(domain.TargetEnabled) {
		t.Fatalf("the supervisor must be able to enable a destination again: %d %v", reenabled.status, reenabled.body)
	}

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "web_1080p",
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("submitting the render failed: %d %v", submitted.status, submitted.body)
	}
	jobID := h.stringField(submitted, "id")
	if _, err := h.app.Workers.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("worker cycle: %v", err)
	}
	dispatched := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/deliveries/dispatch", editorToken, nil, nil)
	if dispatched.status != http.StatusOK {
		t.Fatalf("an editor must still distribute their own master: %d %v", dispatched.status, dispatched.body)
	}
	if dispatched.body["confirmed"].(float64) != 1 {
		t.Fatalf("the approved destination must receive the master: %v", dispatched.body)
	}
}
