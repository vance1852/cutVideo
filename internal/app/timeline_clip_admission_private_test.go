package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

// Footage that was taken out of editorial use must be refused at the moment a
// clip is added, not much later when the cut is sealed.
func TestQuarantinedFootageCannotEnterADraft(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	project := h.call(http.MethodPost, "/api/v1/projects", editorToken, map[string]any{
		"code":        "REELA1",
		"title":       "Autumn Campaign",
		"frame_rate":  25,
		"resolution":  "1920x1080",
		"deadline_at": h.clk.Now().Add(96 * time.Hour).Format(time.RFC3339),
	}, nil)
	if project.status != http.StatusCreated {
		t.Fatalf("creating the project failed: %d %v", project.status, project.body)
	}
	projectID := h.stringField(project, "id")

	ingest := func(filename, checksum string) string {
		created := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/assets", editorToken, map[string]any{
			"filename":          filename,
			"format":            "mov",
			"kind":              "video",
			"declared_checksum": checksum,
			"bytes":             1 << 24,
			"duration_ms":       120_000,
		}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("ingesting %s failed: %d %v", filename, created.status, created.body)
		}
		assetID := h.stringField(created, "id")
		verified := h.call(http.MethodPost, "/api/v1/assets/"+assetID+"/verify", editorToken, map[string]any{
			"observed_checksum": checksum,
		}, nil)
		if verified.status != http.StatusOK || verified.body["status"] != string(domain.AssetVerified) {
			t.Fatalf("verifying %s failed: %d %v", filename, verified.status, verified.body)
		}
		return assetID
	}

	goodAsset := ingest("a001_c001.mov", strings.Repeat("a", 64))
	badAsset := ingest("a002_c007.mov", strings.Repeat("b", 64))

	quarantined := h.call(http.MethodPost, "/api/v1/assets/"+badAsset+"/quarantine", editorToken, map[string]any{
		"reason": "audio drift against the guide track",
	}, nil)
	if quarantined.status != http.StatusOK || quarantined.body["status"] != string(domain.AssetQuarantine) {
		t.Fatalf("quarantining the reel failed: %d %v", quarantined.status, quarantined.body)
	}

	draft := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/timelines", editorToken, map[string]any{
		"notes": "offline assembly",
	}, nil)
	if draft.status != http.StatusCreated {
		t.Fatalf("opening the draft failed: %d %v", draft.status, draft.body)
	}
	timelineID := h.stringField(draft, "id")

	accepted := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/clips", editorToken, map[string]any{
		"asset_id":      goodAsset,
		"order_index":   0,
		"source_in_ms":  0,
		"source_out_ms": 20_000,
		"track":         "program",
		"speed_percent": 100,
	}, nil)
	if accepted.status != http.StatusCreated {
		t.Fatalf("verified footage must be accepted: %d %v", accepted.status, accepted.body)
	}
	if accepted.body["total_duration_ms"].(float64) != 20_000 {
		t.Fatalf("the accepted clip must set the timeline duration: %v", accepted.body)
	}

	refused := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/clips", editorToken, map[string]any{
		"asset_id":      badAsset,
		"order_index":   1,
		"source_in_ms":  0,
		"source_out_ms": 15_000,
		"track":         "program",
		"speed_percent": 100,
	}, nil)
	if refused.status != http.StatusPreconditionFailed {
		t.Fatalf("quarantined footage must be refused at admission, got %d %v", refused.status, refused.body)
	}

	current := h.call(http.MethodGet, "/api/v1/timelines/"+timelineID, editorToken, nil, nil)
	if current.body["clip_count"].(float64) != 1 {
		t.Fatalf("the refused clip must not be stored: %v", current.body)
	}
	if current.body["total_duration_ms"].(float64) != 20_000 {
		t.Fatalf("the refused clip must not be counted in the duration: %v", current.body)
	}

	sealed := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/seal", editorToken, nil, nil)
	if sealed.status != http.StatusOK || sealed.body["status"] != string(domain.TimelineSealed) {
		t.Fatalf("a cut built from usable footage must seal: %d %v", sealed.status, sealed.body)
	}
	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "web_1080p",
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("the sealed cut must be renderable: %d %v", submitted.status, submitted.body)
	}
}
