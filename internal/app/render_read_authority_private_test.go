package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
)

// Render detail and delivery bookkeeping stay inside the team that owns the cut.
func TestForeignEditorCannotReadAnotherTeamsRender(t *testing.T) {
	const (
		foreignEmail    = "neighbour@cutvideo.test"
		foreignPassword = "neighbour-secret-12"
	)

	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	ownerToken := h.provisionEditor(supervisorToken)
	ownerProject, ownerTimeline := h.sealedCut(ownerToken, "REELE1")

	created := h.call(http.MethodPost, "/api/v1/users", supervisorToken, map[string]string{
		"email":        foreignEmail,
		"display_name": "Neighbour Editor",
		"role":         string(domain.RoleEditor),
		"password":     foreignPassword,
	}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("provisioning the neighbouring editor failed: %d %v", created.status, created.body)
	}
	foreignToken := h.signIn(foreignEmail, foreignPassword)

	target := h.call(http.MethodPost, "/api/v1/projects/"+ownerProject+"/delivery-targets", supervisorToken, map[string]any{
		"name":           "client-drop",
		"kind":           "s3",
		"endpoint":       "https://client.invalid/drop",
		"credential_ref": "vault://client",
	}, nil)
	if target.status != http.StatusCreated {
		t.Fatalf("creating the destination failed: %d %v", target.status, target.body)
	}

	submitted := h.call(http.MethodPost, "/api/v1/renders", ownerToken, map[string]any{
		"timeline_id": ownerTimeline,
		"preset":      "master_2160p",
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("submitting the render failed: %d %v", submitted.status, submitted.body)
	}
	jobID := h.stringField(submitted, "id")
	if _, err := h.app.Workers.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("worker cycle: %v", err)
	}

	peekedRender := h.call(http.MethodGet, "/api/v1/renders/"+jobID, foreignToken, nil, nil)
	if peekedRender.status != http.StatusForbidden {
		t.Fatalf("a foreign editor must not read this render, got %d %v", peekedRender.status, peekedRender.body)
	}
	if peekedRender.body["output_uri"] != nil {
		t.Fatalf("the artefact reference must not leak: %v", peekedRender.body)
	}
	peekedDeliveries := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", foreignToken, nil, nil)
	if peekedDeliveries.status != http.StatusForbidden {
		t.Fatalf("a foreign editor must not read the delivery records, got %d %v", peekedDeliveries.status, peekedDeliveries.body)
	}
	if peekedDeliveries.body["items"] != nil {
		t.Fatalf("the delivery bookkeeping must not leak: %v", peekedDeliveries.body)
	}

	mine := h.call(http.MethodGet, "/api/v1/renders/"+jobID, ownerToken, nil, nil)
	if mine.status != http.StatusOK {
		t.Fatalf("the owning editor must read their render: %d %v", mine.status, mine.body)
	}
	if uri, ok := mine.body["output_uri"].(string); !ok || uri == "" {
		t.Fatalf("the owning editor must see the artefact reference: %v", mine.body)
	}
	myRecords := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", ownerToken, nil, nil)
	if myRecords.status != http.StatusOK || myRecords.body["total"].(float64) != 1 {
		t.Fatalf("the owning editor must read the delivery records: %d %v", myRecords.status, myRecords.body)
	}
	supervised := h.call(http.MethodGet, "/api/v1/renders/"+jobID, supervisorToken, nil, nil)
	if supervised.status != http.StatusOK {
		t.Fatalf("a supervisor must read any render: %d %v", supervised.status, supervised.body)
	}
	supervisedRecords := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", supervisorToken, nil, nil)
	if supervisedRecords.status != http.StatusOK {
		t.Fatalf("a supervisor must read any delivery records: %d %v", supervisedRecords.status, supervisedRecords.body)
	}

	_, foreignTimeline := h.sealedCut(foreignToken, "REELE2")
	own := h.call(http.MethodPost, "/api/v1/renders", foreignToken, map[string]any{
		"timeline_id": foreignTimeline,
		"preset":      "web_1080p",
	}, nil)
	if own.status != http.StatusAccepted {
		t.Fatalf("the neighbouring editor must still submit their own render: %d %v", own.status, own.body)
	}
	readOwn := h.call(http.MethodGet, "/api/v1/renders/"+h.stringField(own, "id"), foreignToken, nil, nil)
	if readOwn.status != http.StatusOK {
		t.Fatalf("the neighbouring editor must read their own render: %d %v", readOwn.status, readOwn.body)
	}
}
