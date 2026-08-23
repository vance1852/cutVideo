package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
)

// A frozen project must refuse editorial writes until it is unlocked again.
func TestLockedProjectRefusesEditorialWrites(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, _ := h.sealedCut(editorToken, "REEL71")

	assets := h.call(http.MethodGet, "/api/v1/projects/"+projectID+"/assets", editorToken, nil, nil)
	assetID := assets.body["items"].([]any)[0].(map[string]any)["id"].(string)

	draft := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/timelines", editorToken, map[string]any{
		"notes": "next revision",
	}, nil)
	if draft.status != http.StatusCreated {
		t.Fatalf("opening the next draft failed: %d %v", draft.status, draft.body)
	}
	draftID := h.stringField(draft, "id")

	locked := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/lock", editorToken, nil, nil)
	if locked.status != http.StatusOK || locked.body["status"] != string(domain.ProjectLocked) {
		t.Fatalf("freezing the project failed: %d %v", locked.status, locked.body)
	}

	clipBody := func(order int, in, out int64) map[string]any {
		return map[string]any{
			"asset_id":      assetID,
			"order_index":   order,
			"source_in_ms":  in,
			"source_out_ms": out,
			"track":         "program",
			"speed_percent": 100,
		}
	}

	frozenClip := h.call(http.MethodPost, "/api/v1/timelines/"+draftID+"/clips", editorToken, clipBody(0, 0, 12_000), nil)
	if frozenClip.status != http.StatusPreconditionFailed {
		t.Fatalf("a frozen project must refuse new clips, got %d %v", frozenClip.status, frozenClip.body)
	}
	frozenAsset := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/assets", editorToken, map[string]any{
		"filename":          "b002_c001.mov",
		"format":            "mov",
		"kind":              "video",
		"declared_checksum": strings.Repeat("e", 64),
		"bytes":             1 << 22,
		"duration_ms":       60_000,
	}, nil)
	if frozenAsset.status != http.StatusPreconditionFailed {
		t.Fatalf("a frozen project must refuse new footage, got %d %v", frozenAsset.status, frozenAsset.body)
	}
	frozenSeal := h.call(http.MethodPost, "/api/v1/timelines/"+draftID+"/seal", editorToken, nil, nil)
	if frozenSeal.status != http.StatusPreconditionFailed {
		t.Fatalf("a frozen project must refuse sealing, got %d %v", frozenSeal.status, frozenSeal.body)
	}

	stillFrozen := h.call(http.MethodGet, "/api/v1/projects/"+projectID, editorToken, nil, nil)
	if stillFrozen.body["status"] != string(domain.ProjectLocked) {
		t.Fatalf("the project must stay frozen: %v", stillFrozen.body)
	}
	if stillFrozen.body["sealed_version"].(float64) != 1 {
		t.Fatalf("the sealed pointer must not move while frozen: %v", stillFrozen.body)
	}

	unlocked := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/unlock", editorToken, nil, nil)
	if unlocked.status != http.StatusOK || unlocked.body["status"] != string(domain.ProjectActive) {
		t.Fatalf("unfreezing the project failed: %d %v", unlocked.status, unlocked.body)
	}

	added := h.call(http.MethodPost, "/api/v1/timelines/"+draftID+"/clips", editorToken, clipBody(0, 0, 12_000), nil)
	if added.status != http.StatusCreated {
		t.Fatalf("an unfrozen project must accept clips again: %d %v", added.status, added.body)
	}
	if added.body["total_duration_ms"].(float64) != 12_000 {
		t.Fatalf("the accepted clip must count towards the timeline duration: %v", added.body)
	}
	sealed := h.call(http.MethodPost, "/api/v1/timelines/"+draftID+"/seal", editorToken, nil, nil)
	if sealed.status != http.StatusOK || sealed.body["status"] != string(domain.TimelineSealed) {
		t.Fatalf("an unfrozen project must accept sealing again: %d %v", sealed.status, sealed.body)
	}
	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": draftID,
		"preset":      "web_1080p",
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("the newly sealed cut must be renderable: %d %v", submitted.status, submitted.body)
	}
}
