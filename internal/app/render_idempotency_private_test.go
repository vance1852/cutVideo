package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/cutVideo/internal/domain"
)

// An idempotency key belongs to one project. Two projects using the same key must
// each get their own render, and a replay inside one project must still collapse.
func TestSharedIdempotencyKeyDoesNotLeakRendersAcrossProjects(t *testing.T) {
	const (
		secondEmail    = "second-editor@cutvideo.test"
		secondPassword = "second-editor-secret-7"
		sharedKey      = "nightly-master"
	)

	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	firstToken := h.provisionEditor(supervisorToken)
	firstProject, firstTimeline := h.sealedCut(firstToken, "REEL31")

	created := h.call(http.MethodPost, "/api/v1/users", supervisorToken, map[string]string{
		"email":        secondEmail,
		"display_name": "Second Cut Editor",
		"role":         string(domain.RoleEditor),
		"password":     secondPassword,
	}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("provisioning the second editor failed: %d %v", created.status, created.body)
	}
	secondToken := h.signIn(secondEmail, secondPassword)
	secondProject, secondTimeline := h.sealedCut(secondToken, "REEL32")

	first := h.call(http.MethodPost, "/api/v1/renders", firstToken, map[string]any{
		"timeline_id": firstTimeline,
		"preset":      "web_1080p",
		"priority":    50,
	}, map[string]string{"Idempotency-Key": sharedKey})
	if first.status != http.StatusAccepted {
		t.Fatalf("the first submission failed: %d %v", first.status, first.body)
	}
	firstJob := h.stringField(first, "id")

	second := h.call(http.MethodPost, "/api/v1/renders", secondToken, map[string]any{
		"timeline_id": secondTimeline,
		"preset":      "web_1080p",
		"priority":    50,
	}, map[string]string{"Idempotency-Key": sharedKey})
	if second.status != http.StatusAccepted {
		t.Fatalf("the second submission failed: %d %v", second.status, second.body)
	}
	secondJob := h.stringField(second, "id")

	if secondJob == firstJob {
		t.Fatalf("a shared key must not hand another project's render back, both got %s", firstJob)
	}
	if second.body["project_id"] != secondProject {
		t.Fatalf("the second submission must belong to its own project %s: %v", secondProject, second.body)
	}
	if second.body["timeline_id"] != secondTimeline {
		t.Fatalf("the second submission must render its own sealed cut %s: %v", secondTimeline, second.body)
	}
	if first.body["project_id"] != firstProject {
		t.Fatalf("the first submission must belong to its own project %s: %v", firstProject, first.body)
	}

	for _, owner := range []struct {
		token string
		job   string
	}{{firstToken, firstJob}, {secondToken, secondJob}} {
		listed := h.call(http.MethodGet, "/api/v1/renders", owner.token, nil, nil)
		if listed.status != http.StatusOK {
			t.Fatalf("listing renders failed: %d %v", listed.status, listed.body)
		}
		if listed.body["total"].(float64) != 1 {
			t.Fatalf("each editor must see exactly their own queued render: %v", listed.body)
		}
		items := listed.body["items"].([]any)
		if items[0].(map[string]any)["id"] != owner.job {
			t.Fatalf("expected render %s in the listing, got %v", owner.job, items[0])
		}
	}

	replay := h.call(http.MethodPost, "/api/v1/renders", firstToken, map[string]any{
		"timeline_id": firstTimeline,
		"preset":      "web_1080p",
		"priority":    50,
	}, map[string]string{"Idempotency-Key": sharedKey})
	if replay.status != http.StatusAccepted {
		t.Fatalf("replaying the key inside the same project failed: %d %v", replay.status, replay.body)
	}
	if h.stringField(replay, "id") != firstJob {
		t.Fatalf("a replay inside the same project must return the original render %s: %v", firstJob, replay.body)
	}

	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", supervisorToken, nil, nil)
	if capacity.body["queued_jobs"].(float64) != 2 {
		t.Fatalf("both sealed cuts must be queued on the farm: %v", capacity.body)
	}
	claimedJobs := map[string]bool{}
	for index := 0; index < 2; index++ {
		claimed, err := h.app.Render.Claim(context.Background(), "primary")
		if err != nil {
			t.Fatalf("claim %d failed: %v", index, err)
		}
		if claimed == nil {
			t.Fatalf("claim %d returned no job", index)
		}
		claimedJobs[claimed.ID] = true
	}
	if !claimedJobs[firstJob] || !claimedJobs[secondJob] {
		t.Fatalf("both renders must reach the farm, claimed %v", claimedJobs)
	}
}
