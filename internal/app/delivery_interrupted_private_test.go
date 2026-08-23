package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/service/delivery"
)

// A transfer that is cut short must still be settled, so the destination can be
// retried once it is reachable again.
func TestInterruptedDeliveryIsSettledAndCanBeRetried(t *testing.T) {
	transport := &delivery.StubTransport{Failures: map[string]error{
		"partner": context.DeadlineExceeded,
	}}
	h := newHarness(t, harnessOptions{transport: transport})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REEL61")

	target := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", supervisorToken, map[string]any{
		"name":           "partner",
		"kind":           "aspera",
		"endpoint":       "https://partner.invalid/ingest",
		"credential_ref": "vault://partner",
	}, nil)
	if target.status != http.StatusCreated {
		t.Fatalf("creating the destination failed: %d %v", target.status, target.body)
	}

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "master_2160p",
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("submitting the render failed: %d %v", submitted.status, submitted.body)
	}
	jobID := h.stringField(submitted, "id")
	if _, err := h.app.Workers.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("worker cycle: %v", err)
	}

	record := func() map[string]any {
		listed := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", editorToken, nil, nil)
		if listed.status != http.StatusOK {
			t.Fatalf("listing delivery records failed: %d %v", listed.status, listed.body)
		}
		items := listed.body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected exactly one delivery record: %v", listed.body)
		}
		return items[0].(map[string]any)
	}

	interrupted := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/deliveries/dispatch", editorToken, nil, nil)
	if interrupted.status != http.StatusOK {
		t.Fatalf("the interrupted round must answer with a result envelope, got %d %v", interrupted.status, interrupted.body)
	}
	if interrupted.body["failed"].(float64) != 1 {
		t.Fatalf("the interrupted transfer must be reported as failed: %v", interrupted.body)
	}
	settled := record()
	if settled["status"] != string(domain.DeliveryFailed) {
		t.Fatalf("an interrupted transfer must be settled as failed, got %v", settled)
	}
	if settled["failure"] == nil || settled["failure"] == "" {
		t.Fatalf("the settled record must carry the failure reason: %v", settled)
	}
	if settled["attempt"].(float64) != 1 {
		t.Fatalf("the interrupted transfer must count as one attempt: %v", settled)
	}

	transport.Failures = map[string]error{}
	retried := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/deliveries/dispatch", editorToken, nil, nil)
	if retried.status != http.StatusOK {
		t.Fatalf("the retry must answer with a result envelope, got %d %v", retried.status, retried.body)
	}
	if retried.body["confirmed"].(float64) != 1 {
		t.Fatalf("a recovered destination must confirm on retry: %v", retried.body)
	}
	confirmed := record()
	if confirmed["status"] != string(domain.DeliveryConfirmed) {
		t.Fatalf("the record must be confirmed after the retry: %v", confirmed)
	}
	if len(transport.Sent) != 1 {
		t.Fatalf("exactly one successful outbound transfer was expected, saw %d", len(transport.Sent))
	}

	project := h.call(http.MethodGet, "/api/v1/projects/"+projectID, editorToken, nil, nil)
	if project.body["status"] != string(domain.ProjectDelivered) {
		t.Fatalf("the project must reach the delivered state: %v", project.body)
	}
}
