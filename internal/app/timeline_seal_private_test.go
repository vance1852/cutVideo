package app_test

import (
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
)

// Sealing a newer cut must retire the previous one, not itself.
func TestSecondSealSupersedesOnlyThePreviousVersion(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, firstTimeline := h.sealedCut(editorToken, "REEL41")

	assets := h.call(http.MethodGet, "/api/v1/projects/"+projectID+"/assets", editorToken, nil, nil)
	if assets.status != http.StatusOK {
		t.Fatalf("listing footage failed: %d %v", assets.status, assets.body)
	}
	assetItems := assets.body["items"].([]any)
	if len(assetItems) != 1 {
		t.Fatalf("expected the ingested footage to be listed: %v", assets.body)
	}
	assetID := assetItems[0].(map[string]any)["id"].(string)

	draft := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/timelines", editorToken, map[string]any{
		"notes": "client revision",
	}, nil)
	if draft.status != http.StatusCreated {
		t.Fatalf("opening the second draft failed: %d %v", draft.status, draft.body)
	}
	secondTimeline := h.stringField(draft, "id")

	for index, clip := range []struct {
		in, out int64
		track   string
	}{
		{0, 18_000, "program"},
		{18_000, 40_000, "program"},
	} {
		added := h.call(http.MethodPost, "/api/v1/timelines/"+secondTimeline+"/clips", editorToken, map[string]any{
			"asset_id":      assetID,
			"order_index":   index,
			"source_in_ms":  clip.in,
			"source_out_ms": clip.out,
			"track":         clip.track,
			"speed_percent": 100,
		}, nil)
		if added.status != http.StatusCreated {
			t.Fatalf("adding clip %d to the second draft failed: %d %v", index, added.status, added.body)
		}
	}

	sealed := h.call(http.MethodPost, "/api/v1/timelines/"+secondTimeline+"/seal", editorToken, nil, nil)
	if sealed.status != http.StatusOK {
		t.Fatalf("sealing the second version failed: %d %v", sealed.status, sealed.body)
	}
	if sealed.body["status"] != string(domain.TimelineSealed) {
		t.Fatalf("the newly sealed version must be sealed, got %v", sealed.body)
	}

	current := h.call(http.MethodGet, "/api/v1/timelines/"+secondTimeline, editorToken, nil, nil)
	if current.body["status"] != string(domain.TimelineSealed) {
		t.Fatalf("the newest version must stay sealed after the transaction: %v", current.body)
	}
	previous := h.call(http.MethodGet, "/api/v1/timelines/"+firstTimeline, editorToken, nil, nil)
	if previous.body["status"] != string(domain.TimelineSuperseded) {
		t.Fatalf("the previous version must be superseded: %v", previous.body)
	}
	project := h.call(http.MethodGet, "/api/v1/projects/"+projectID, editorToken, nil, nil)
	if project.body["sealed_version"].(float64) != 2 {
		t.Fatalf("the project must point at the newest sealed version: %v", project.body)
	}

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": secondTimeline,
		"preset":      "web_1080p",
		"priority":    50,
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("the newest sealed cut must be renderable: %d %v", submitted.status, submitted.body)
	}
	if submitted.body["timeline_id"] != secondTimeline {
		t.Fatalf("the render must target the newest cut: %v", submitted.body)
	}

	stale := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": firstTimeline,
		"preset":      "web_1080p",
		"priority":    50,
	}, nil)
	if stale.status != http.StatusPreconditionFailed {
		t.Fatalf("a superseded cut must not be renderable, got %d %v", stale.status, stale.body)
	}
}
