package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
)

// A render belongs to the operator who submitted it: only they or a supervisor
// may cancel it.
func TestOnlyTheOwnerOrASupervisorCancelsARender(t *testing.T) {
	const (
		foreignEmail    = "other-team@cutvideo.test"
		foreignPassword = "other-team-secret-8"
	)

	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	ownerToken := h.provisionEditor(supervisorToken)

	created := h.call(http.MethodPost, "/api/v1/users", supervisorToken, map[string]string{
		"email":        foreignEmail,
		"display_name": "Other Team Editor",
		"role":         string(domain.RoleEditor),
		"password":     foreignPassword,
	}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("provisioning the other team's editor failed: %d %v", created.status, created.body)
	}
	foreignToken := h.signIn(foreignEmail, foreignPassword)

	startRender := func(code string) string {
		_, timelineID := h.sealedCut(ownerToken, code)
		submitted := h.call(http.MethodPost, "/api/v1/renders", ownerToken, map[string]any{
			"timeline_id": timelineID,
			"preset":      "master_2160p",
		}, nil)
		if submitted.status != http.StatusAccepted {
			t.Fatalf("submitting %s failed: %d %v", code, submitted.status, submitted.body)
		}
		if _, err := h.app.Render.Claim(context.Background(), "primary"); err != nil {
			t.Fatalf("claiming %s failed: %v", code, err)
		}
		return h.stringField(submitted, "id")
	}

	jobID := startRender("REELD1")

	refused := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/cancel", foreignToken, map[string]any{
		"reason": "tidying the queue",
	}, nil)
	if refused.status != http.StatusForbidden {
		t.Fatalf("an unrelated editor must not cancel this render, got %d %v", refused.status, refused.body)
	}

	untouched := h.call(http.MethodGet, "/api/v1/renders/"+jobID, ownerToken, nil, nil)
	if untouched.body["status"] == string(domain.RenderCanceled) {
		t.Fatalf("the render must survive the refused cancellation: %v", untouched.body)
	}
	if untouched.body["slot_id"] == nil || untouched.body["slot_id"] == "" {
		t.Fatalf("the render must keep the seat it holds: %v", untouched.body)
	}
	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", supervisorToken, nil, nil)
	if capacity.body["busy"].(float64) != 1 {
		t.Fatalf("the seat must still be busy after the refusal: %v", capacity.body)
	}

	byOwner := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/cancel", ownerToken, map[string]any{
		"reason": "client pulled the cut",
	}, nil)
	if byOwner.status != http.StatusOK || byOwner.body["status"] != string(domain.RenderCanceled) {
		t.Fatalf("the operator who submitted the render must be able to cancel it: %d %v", byOwner.status, byOwner.body)
	}
	freed := h.call(http.MethodGet, "/api/v1/render-farm/capacity", supervisorToken, nil, nil)
	if freed.body["busy"].(float64) != 0 {
		t.Fatalf("cancelling must release the seat: %v", freed.body)
	}

	secondJob := startRender("REELD2")
	bySupervisor := h.call(http.MethodPost, "/api/v1/renders/"+secondJob+"/cancel", supervisorToken, map[string]any{
		"reason": "farm maintenance window",
	}, nil)
	if bySupervisor.status != http.StatusOK || bySupervisor.body["status"] != string(domain.RenderCanceled) {
		t.Fatalf("a supervisor must be able to arbitrate any render: %d %v", bySupervisor.status, bySupervisor.body)
	}
}
