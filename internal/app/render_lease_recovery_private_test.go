package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/worker"
)

// Once housekeeping has returned a stalled render to the queue, the encoder that
// lost the lease must not be able to publish its abandoned artefact.
func TestRecoveredLeaseIsNotCompletedByTheStaleWorker(t *testing.T) {
	var current *harness
	recovered := -1
	renderer := worker.RendererFunc(func(_ context.Context, job *domain.RenderJob) (worker.Output, error) {
		if recovered < 0 {
			current.clk.Advance(2 * time.Minute)
			report := current.app.Reaper.RunOnce(context.Background())
			if report.ObservedFailure != nil {
				return worker.Output{}, report.ObservedFailure
			}
			recovered = report.RequeuedJobs
		}
		return worker.Output{URI: "cutvideo://renders/" + job.ID + ".mov", Bytes: 9 << 20}, nil
	})

	h := newHarness(t, harnessOptions{leaseTTL: 30 * time.Second, renderer: renderer})
	current = h
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REEL22")

	target := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", supervisorToken, map[string]any{
		"name":           "broadcast",
		"kind":           "webhook",
		"endpoint":       "https://broadcast.invalid/ingest",
		"credential_ref": "vault://broadcast",
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

	processed, _ := h.app.Workers.ProcessOnce(context.Background())
	if recovered != 1 {
		t.Fatalf("housekeeping must recover exactly one stalled render, got %d", recovered)
	}
	if processed {
		t.Fatal("a render whose lease was recovered must not be reported as processed")
	}

	stale := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if stale.body["status"] != string(domain.RenderQueued) {
		t.Fatalf("the recovered render must stay queued: %v", stale.body)
	}
	if uri, ok := stale.body["output_uri"].(string); ok && uri != "" {
		t.Fatalf("an abandoned attempt must not publish an artefact: %v", stale.body)
	}
	records := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", editorToken, nil, nil)
	if records.body["total"].(float64) != 0 {
		t.Fatalf("no downstream delivery may be created for an abandoned attempt: %v", records.body)
	}
	completions := h.call(http.MethodGet, "/api/v1/audit-events?action="+domain.ActionRenderComplete, supervisorToken, nil, nil)
	if completions.body["total"].(float64) != 0 {
		t.Fatalf("no completion audit event may be written for an abandoned attempt: %v", completions.body)
	}

	h.clk.Advance(2 * time.Minute)
	finished, err := h.app.Workers.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("the second worker cycle failed: %v", err)
	}
	if !finished {
		t.Fatal("the requeued render must be picked up again")
	}
	done := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if done.body["status"] != string(domain.RenderSucceeded) {
		t.Fatalf("the requeued attempt must finish: %v", done.body)
	}
	if done.body["attempt"].(float64) != 2 {
		t.Fatalf("the requeued attempt must count as the second attempt: %v", done.body)
	}
	delivered := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", editorToken, nil, nil)
	if delivered.body["total"].(float64) != 1 {
		t.Fatalf("the successful attempt must create the downstream delivery record: %v", delivered.body)
	}
}
