package app_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/service/delivery"
)

// Retrying a refused destination must accumulate attempts until the budget is
// spent, and must then stop calling the destination altogether.
func TestRefusedDeliveryExhaustsItsAttemptBudget(t *testing.T) {
	transport := &delivery.StubTransport{Failures: map[string]error{
		"archive": errors.New("archive endpoint refused the transfer"),
	}}
	h := newHarness(t, harnessOptions{transport: transport})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REEL51")

	targets := map[string]string{}
	for _, entry := range []struct{ name, kind string }{{"broadcast", "webhook"}, {"archive", "aspera"}} {
		created := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", supervisorToken, map[string]any{
			"name":           entry.name,
			"kind":           entry.kind,
			"endpoint":       "https://" + entry.name + ".invalid/ingest",
			"credential_ref": "vault://" + entry.name,
		}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("creating destination %s failed: %d %v", entry.name, created.status, created.body)
		}
		targets[entry.name] = h.stringField(created, "id")
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

	recordFor := func(name string) map[string]any {
		listed := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", editorToken, nil, nil)
		if listed.status != http.StatusOK {
			t.Fatalf("listing delivery records failed: %d %v", listed.status, listed.body)
		}
		for _, raw := range listed.body["items"].([]any) {
			record := raw.(map[string]any)
			if record["target_id"] == targets[name] {
				return record
			}
		}
		t.Fatalf("no delivery record for destination %s: %v", name, listed.body)
		return nil
	}
	outcomeFor := func(answer response, name string) map[string]any {
		for _, raw := range answer.body["outcomes"].([]any) {
			outcome := raw.(map[string]any)
			if outcome["target_name"] == name {
				return outcome
			}
		}
		t.Fatalf("no outcome for destination %s: %v", name, answer.body)
		return nil
	}

	for round := 1; round <= 3; round++ {
		dispatch := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/deliveries/dispatch", editorToken, nil, nil)
		if dispatch.status != http.StatusMultiStatus {
			t.Fatalf("round %d must report a partial delivery, got %d %v", round, dispatch.status, dispatch.body)
		}
		if outcomeFor(dispatch, "archive")["status"] != string(domain.DeliveryFailed) {
			t.Fatalf("round %d: the refusing destination must be reported as failed: %v", round, dispatch.body)
		}
		archive := recordFor("archive")
		if attempt := archive["attempt"].(float64); attempt != float64(round) {
			t.Fatalf("round %d must be counted as attempt %d, got %v", round, round, attempt)
		}
	}

	final := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/deliveries/dispatch", editorToken, nil, nil)
	if final.status != http.StatusMultiStatus {
		t.Fatalf("the exhausted round must still report a partial delivery, got %d %v", final.status, final.body)
	}
	exhausted := outcomeFor(final, "archive")
	if exhausted["failure"] != "delivery attempts are exhausted" {
		t.Fatalf("the destination must be reported as out of attempts: %v", exhausted)
	}
	archive := recordFor("archive")
	if archive["attempt"].(float64) != 3 {
		t.Fatalf("no further attempt may be spent once the budget is used up: %v", archive)
	}

	broadcast := recordFor("broadcast")
	if broadcast["status"] != string(domain.DeliveryConfirmed) {
		t.Fatalf("the reachable destination must stay confirmed: %v", broadcast)
	}
	if len(transport.Sent) != 1 {
		t.Fatalf("a confirmed destination must not be pushed again, saw %d outbound calls", len(transport.Sent))
	}
}
