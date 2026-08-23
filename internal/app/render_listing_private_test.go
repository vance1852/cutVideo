package app_test

import (
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
)

// The listing envelope must describe exactly the rows the caller may see.
func TestRenderListingTotalMatchesTheCallersScope(t *testing.T) {
	const (
		otherEmail    = "colleague@cutvideo.test"
		otherPassword = "colleague-secret-33"
	)

	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	ownToken := h.provisionEditor(supervisorToken)

	created := h.call(http.MethodPost, "/api/v1/users", supervisorToken, map[string]string{
		"email":        otherEmail,
		"display_name": "Colleague Editor",
		"role":         string(domain.RoleEditor),
		"password":     otherPassword,
	}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("provisioning the colleague failed: %d %v", created.status, created.body)
	}
	otherToken := h.signIn(otherEmail, otherPassword)

	submit := func(token, code string) string {
		_, timelineID := h.sealedCut(token, code)
		answer := h.call(http.MethodPost, "/api/v1/renders", token, map[string]any{
			"timeline_id": timelineID,
			"preset":      "proxy_540p",
			"priority":    50,
		}, nil)
		if answer.status != http.StatusAccepted {
			t.Fatalf("submitting %s failed: %d %v", code, answer.status, answer.body)
		}
		return h.stringField(answer, "id")
	}

	mine := map[string]bool{
		submit(ownToken, "REEL91"): true,
		submit(ownToken, "REEL92"): true,
	}
	theirs := submit(otherToken, "REEL93")

	own := h.call(http.MethodGet, "/api/v1/renders", ownToken, nil, nil)
	if own.status != http.StatusOK {
		t.Fatalf("listing my renders failed: %d %v", own.status, own.body)
	}
	items := own.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected my two renders, got %d: %v", len(items), own.body)
	}
	if own.body["total"].(float64) != 2 {
		t.Fatalf("the total must cover only my renders: %v", own.body)
	}
	for _, raw := range items {
		if id := raw.(map[string]any)["id"].(string); !mine[id] {
			t.Fatalf("render %s does not belong to me: %v", id, own.body)
		}
	}

	colleague := h.call(http.MethodGet, "/api/v1/renders", otherToken, nil, nil)
	if colleague.body["total"].(float64) != 1 {
		t.Fatalf("my colleague must only be counted their own render: %v", colleague.body)
	}
	if colleague.body["items"].([]any)[0].(map[string]any)["id"] != theirs {
		t.Fatalf("my colleague must see their own render %s: %v", theirs, colleague.body)
	}

	filtered := h.call(http.MethodGet, "/api/v1/renders?status="+string(domain.RenderQueued), ownToken, nil, nil)
	if filtered.body["total"].(float64) != float64(len(filtered.body["items"].([]any))) {
		t.Fatalf("a filtered total must match the filtered rows: %v", filtered.body)
	}
	if filtered.body["total"].(float64) != 2 {
		t.Fatalf("both of my renders are queued: %v", filtered.body)
	}

	firstPage := h.call(http.MethodGet, "/api/v1/renders?page=1&page_size=1", ownToken, nil, nil)
	if firstPage.body["total"].(float64) != 2 || firstPage.body["total_pages"].(float64) != 2 {
		t.Fatalf("paging must be derived from my own two renders: %v", firstPage.body)
	}
	if len(firstPage.body["items"].([]any)) != 1 {
		t.Fatalf("the first page must carry one render: %v", firstPage.body)
	}
	secondPage := h.call(http.MethodGet, "/api/v1/renders?page=2&page_size=1", ownToken, nil, nil)
	if len(secondPage.body["items"].([]any)) != 1 {
		t.Fatalf("the second page must carry the remaining render: %v", secondPage.body)
	}

	farm := h.call(http.MethodGet, "/api/v1/renders", supervisorToken, nil, nil)
	if farm.body["total"].(float64) != 3 {
		t.Fatalf("a supervisor must see every render: %v", farm.body)
	}
}
