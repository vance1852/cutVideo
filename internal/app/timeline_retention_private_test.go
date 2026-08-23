package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

// Footage that has passed its retention deadline must not be sealed into a cut.
func TestSealRefusesFootagePastItsRetentionWindow(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	buildDraft := func(code, checksum string) (string, string) {
		project := h.call(http.MethodPost, "/api/v1/projects", editorToken, map[string]any{
			"code":        code,
			"title":       "Long Form " + code,
			"frame_rate":  25,
			"resolution":  "1920x1080",
			"deadline_at": h.clk.Now().Add(2000 * time.Hour).Format(time.RFC3339),
		}, nil)
		if project.status != http.StatusCreated {
			t.Fatalf("creating project %s failed: %d %v", code, project.status, project.body)
		}
		projectID := h.stringField(project, "id")

		asset := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/assets", editorToken, map[string]any{
			"filename":          "a001_" + code + ".mov",
			"format":            "mov",
			"kind":              "video",
			"declared_checksum": checksum,
			"bytes":             1 << 24,
			"duration_ms":       120_000,
		}, nil)
		if asset.status != http.StatusCreated {
			t.Fatalf("ingesting footage for %s failed: %d %v", code, asset.status, asset.body)
		}
		assetID := h.stringField(asset, "id")
		verified := h.call(http.MethodPost, "/api/v1/assets/"+assetID+"/verify", editorToken, map[string]any{
			"observed_checksum": checksum,
		}, nil)
		if verified.status != http.StatusOK {
			t.Fatalf("verifying footage for %s failed: %d %v", code, verified.status, verified.body)
		}

		draft := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/timelines", editorToken, map[string]any{
			"notes": "offline cut",
		}, nil)
		if draft.status != http.StatusCreated {
			t.Fatalf("opening the draft for %s failed: %d %v", code, draft.status, draft.body)
		}
		timelineID := h.stringField(draft, "id")
		added := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/clips", editorToken, map[string]any{
			"asset_id":      assetID,
			"order_index":   0,
			"source_in_ms":  0,
			"source_out_ms": 30_000,
			"track":         "program",
			"speed_percent": 100,
		}, nil)
		if added.status != http.StatusCreated {
			t.Fatalf("adding a clip for %s failed: %d %v", code, added.status, added.body)
		}
		return projectID, timelineID
	}

	agedProject, agedTimeline := buildDraft("REELC1", strings.Repeat("c", 64))

	// The cut sits around until the footage it references ages out. The editor
	// signs in again after the break, as they would the next working week.
	h.clk.Advance(300 * time.Hour)
	editorToken = h.signIn(editorEmail, editorPassword)

	refused := h.call(http.MethodPost, "/api/v1/timelines/"+agedTimeline+"/seal", editorToken, nil, nil)
	if refused.status != http.StatusPreconditionFailed {
		t.Fatalf("a cut referencing expired footage must not seal, got %d %v", refused.status, refused.body)
	}
	stillDraft := h.call(http.MethodGet, "/api/v1/timelines/"+agedTimeline, editorToken, nil, nil)
	if stillDraft.body["status"] != string(domain.TimelineDraft) {
		t.Fatalf("the refused cut must stay a draft: %v", stillDraft.body)
	}
	project := h.call(http.MethodGet, "/api/v1/projects/"+agedProject, editorToken, nil, nil)
	if project.body["sealed_version"].(float64) != 0 {
		t.Fatalf("the project must not advertise a sealed version: %v", project.body)
	}
	blocked := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": agedTimeline,
		"preset":      "web_1080p",
	}, nil)
	if blocked.status != http.StatusPreconditionFailed {
		t.Fatalf("an unsealed cut must not reach the farm, got %d %v", blocked.status, blocked.body)
	}

	_, freshTimeline := buildDraft("REELC2", strings.Repeat("d", 64))
	sealed := h.call(http.MethodPost, "/api/v1/timelines/"+freshTimeline+"/seal", editorToken, nil, nil)
	if sealed.status != http.StatusOK || sealed.body["status"] != string(domain.TimelineSealed) {
		t.Fatalf("footage inside its retention window must still seal: %d %v", sealed.status, sealed.body)
	}
	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": freshTimeline,
		"preset":      "web_1080p",
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("the freshly sealed cut must reach the farm: %d %v", submitted.status, submitted.body)
	}
}
