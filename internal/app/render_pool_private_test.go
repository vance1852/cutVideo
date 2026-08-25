package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/apierr"
)

// A seat belongs to its pool: a full pool reports exhausted capacity instead of
// borrowing hardware that another pool owns.
func TestClaimStaysInsideItsRenderPool(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	provisioned := h.call(http.MethodPost, "/api/v1/render-farm/slots", supervisorToken, map[string]any{
		"name":  "archive-01",
		"pool":  "archive",
		"units": 8,
	}, nil)
	if provisioned.status != http.StatusCreated {
		t.Fatalf("provisioning the dedicated seat failed: %d %v", provisioned.status, provisioned.body)
	}
	archiveSeat := h.stringField(provisioned, "id")

	capacityFor := func(pool string) map[string]any {
		answer := h.call(http.MethodGet, "/api/v1/render-farm/capacity?pool="+pool, supervisorToken, nil, nil)
		if answer.status != http.StatusOK {
			t.Fatalf("reading %s capacity failed: %d %v", pool, answer.status, answer.body)
		}
		return answer.body
	}

	archive := capacityFor("archive")
	if archive["total"].(float64) != 1 || archive["idle"].(float64) != 1 {
		t.Fatalf("the dedicated pool must start with its own idle seat: %v", archive)
	}
	primary := capacityFor("primary")
	if primary["total"].(float64) != 2 || primary["idle"].(float64) != 2 {
		t.Fatalf("the default pool must start with two idle seats: %v", primary)
	}

	for _, code := range []string{"REEL81", "REEL82", "REEL83"} {
		_, timelineID := h.sealedCut(editorToken, code)
		submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
			"timeline_id": timelineID,
			"preset":      "proxy_540p",
			"priority":    50,
		}, nil)
		if submitted.status != http.StatusAccepted {
			t.Fatalf("submitting %s failed: %d %v", code, submitted.status, submitted.body)
		}
	}

	taken := map[string]bool{}
	for index := 0; index < 2; index++ {
		claimed, err := h.app.Render.Claim(context.Background(), "primary")
		if err != nil {
			t.Fatalf("claim %d on the default pool failed: %v", index, err)
		}
		if claimed == nil {
			t.Fatalf("claim %d on the default pool returned no job", index)
		}
		taken[claimed.SlotID] = true
	}
	if taken[archiveSeat] {
		t.Fatalf("the default queue must not take the dedicated seat %s", archiveSeat)
	}

	overflow, err := h.app.Render.Claim(context.Background(), "primary")
	if err == nil {
		t.Fatalf("a full pool must report exhausted capacity, got job %+v", overflow)
	}
	if !apierr.IsCode(err, apierr.CodeQuotaExhausted) {
		t.Fatalf("a full pool must report exhausted capacity, got %v", err)
	}

	archive = capacityFor("archive")
	if archive["busy"].(float64) != 0 || archive["idle"].(float64) != 1 {
		t.Fatalf("the dedicated pool must keep its seat free: %v", archive)
	}
	primary = capacityFor("primary")
	if primary["busy"].(float64) != 2 || primary["idle"].(float64) != 0 {
		t.Fatalf("the default pool must report both of its seats busy: %v", primary)
	}

	dedicated, err := h.app.Render.Claim(context.Background(), "archive")
	if err != nil {
		t.Fatalf("claiming inside the dedicated pool failed: %v", err)
	}
	if dedicated == nil {
		t.Fatalf("the dedicated pool must hand out its own seat")
	}
	if dedicated.SlotID != archiveSeat {
		t.Fatalf("the dedicated claim must use seat %s, got %s", archiveSeat, dedicated.SlotID)
	}

	afterDedicated := capacityFor("archive")
	if afterDedicated["busy"].(float64) != 1 {
		t.Fatalf("the dedicated seat must now carry its own work: %v", afterDedicated)
	}
}
