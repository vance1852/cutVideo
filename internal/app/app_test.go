package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/app"
	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/service/delivery"
	"github.com/vance1852/cutVideo/internal/worker"
)

const (
	supervisorEmail    = "supervisor@cutvideo.test"
	supervisorPassword = "supervisor-secret-1"
	editorEmail        = "editor@cutvideo.test"
	editorPassword     = "editor-secret-42"
)

type harness struct {
	t         *testing.T
	app       *app.App
	server    *httptest.Server
	clk       *clock.Fixed
	transport *delivery.StubTransport
	renderer  worker.Renderer
}

type harnessOptions struct {
	renderer   worker.Renderer
	transport  *delivery.StubTransport
	dsn        string
	maxTries   int
	backoff    time.Duration
	leaseTTL   time.Duration
	leaseRenew time.Duration
	randomIDs  bool
}

func testConfig(dsn string, opts harnessOptions) config.Config {
	cfg := config.Config{
		Env: "test",
		HTTP: config.HTTPConfig{
			Addr:           "127.0.0.1:0",
			RequestTimeout: 5 * time.Second,
			ShutdownGrace:  time.Second,
		},
		Database: config.DatabaseConfig{DSN: dsn, MaxOpenConns: 1, RunMigrations: true},
		Auth: config.AuthConfig{
			SessionTTL:     2 * time.Hour,
			PasswordPepper: "test-pepper",
			IdempotencyTTL: time.Hour,
		},
		Render: config.RenderConfig{
			LeaseTTL:     time.Minute,
			MaxAttempts:  2,
			RetryBackoff: time.Second,
			Presets:      []string{"proxy_540p", "web_1080p", "master_2160p"},
		},
		Worker: config.WorkerConfig{
			Enabled:            false,
			Concurrency:        1,
			PollInterval:       10 * time.Millisecond,
			LeaseRenewInterval: 20 * time.Second,
			ReaperPeriod:       time.Second,
		},
		Media: config.MediaConfig{
			Retention:      240 * time.Hour,
			MaxAssetBytes:  1 << 40,
			AllowedFormats: []string{"mov", "mp4", "mxf"},
		},
		Logging: config.LoggingConfig{Level: "error", Format: "text"},
	}
	if opts.maxTries > 0 {
		cfg.Render.MaxAttempts = opts.maxTries
	}
	if opts.backoff > 0 {
		cfg.Render.RetryBackoff = opts.backoff
	}
	if opts.leaseTTL > 0 {
		cfg.Render.LeaseTTL = opts.leaseTTL
	}
	if opts.leaseRenew > 0 {
		cfg.Worker.LeaseRenewInterval = opts.leaseRenew
	}
	if cfg.Worker.LeaseRenewInterval >= cfg.Render.LeaseTTL {
		cfg.Worker.LeaseRenewInterval = cfg.Render.LeaseTTL / 3
	}
	return cfg
}

func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	dsn := opts.dsn
	if dsn == "" {
		path := filepath.ToSlash(filepath.Join(t.TempDir(), "cutvideo-app.db"))
		dsn = fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path)
	}
	fixed := clock.NewFixed(time.Date(2026, time.May, 4, 9, 0, 0, 0, time.UTC))
	transport := opts.transport
	if transport == nil {
		transport = &delivery.StubTransport{}
	}
	renderer := opts.renderer
	if renderer == nil {
		renderer = worker.SyntheticRenderer{BytesPerSecond: 1 << 20}
	}
	var generator ids.Generator = &ids.SequenceGenerator{}
	if opts.randomIDs {
		generator = ids.RandomGenerator{}
	}
	application, err := app.New(context.Background(), testConfig(dsn, opts), app.Options{
		Clock:     fixed,
		IDs:       generator,
		Renderer:  renderer,
		Transport: transport,
		Logger:    logging.Discard(),
	})
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	if err := application.Bootstrap(context.Background(), supervisorEmail, supervisorPassword); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	server := httptest.NewServer(application.Handler)
	t.Cleanup(func() {
		server.Close()
		if err := application.Close(); err != nil {
			t.Errorf("close app: %v", err)
		}
	})
	return &harness{t: t, app: application, server: server, clk: fixed, transport: transport, renderer: renderer}
}

type response struct {
	status int
	body   map[string]any
	header http.Header
}

func (h *harness) call(method, path, token string, payload any, headers map[string]string) response {
	h.t.Helper()
	var reader *bytes.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatalf("encode payload: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	answer, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatalf("call %s %s: %v", method, path, err)
	}
	defer answer.Body.Close()

	result := response{status: answer.StatusCode, header: answer.Header}
	if answer.ContentLength != 0 {
		decoded := map[string]any{}
		if err := json.NewDecoder(answer.Body).Decode(&decoded); err == nil {
			result.body = decoded
		}
	}
	return result
}

func (h *harness) signIn(email, password string) string {
	h.t.Helper()
	answer := h.call(http.MethodPost, "/api/v1/sessions", "", map[string]string{
		"email":    email,
		"password": password,
	}, nil)
	if answer.status != http.StatusOK {
		h.t.Fatalf("sign in %s failed: %d %v", email, answer.status, answer.body)
	}
	token, ok := answer.body["token"].(string)
	if !ok || token == "" {
		h.t.Fatalf("sign in response carried no token: %v", answer.body)
	}
	return token
}

func (h *harness) provisionEditor(supervisorToken string) string {
	h.t.Helper()
	answer := h.call(http.MethodPost, "/api/v1/users", supervisorToken, map[string]string{
		"email":        editorEmail,
		"display_name": "Cut Editor",
		"role":         string(domain.RoleEditor),
		"password":     editorPassword,
	}, nil)
	if answer.status != http.StatusCreated {
		h.t.Fatalf("provision editor failed: %d %v", answer.status, answer.body)
	}
	return h.signIn(editorEmail, editorPassword)
}

func (h *harness) stringField(answer response, field string) string {
	h.t.Helper()
	value, ok := answer.body[field].(string)
	if !ok {
		h.t.Fatalf("response has no string field %q: %v", field, answer.body)
	}
	return value
}

// sealedCut walks the editorial path and returns the project and sealed timeline
// identifiers.
func (h *harness) sealedCut(editorToken, code string) (string, string) {
	h.t.Helper()
	project := h.call(http.MethodPost, "/api/v1/projects", editorToken, map[string]any{
		"code":        code,
		"title":       "Spring Campaign " + code,
		"frame_rate":  25,
		"resolution":  "1920x1080",
		"deadline_at": h.clk.Now().Add(96 * time.Hour).Format(time.RFC3339),
	}, nil)
	if project.status != http.StatusCreated {
		h.t.Fatalf("create project failed: %d %v", project.status, project.body)
	}
	projectID := h.stringField(project, "id")

	checksum := strings.Repeat("d", 64)
	asset := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/assets", editorToken, map[string]any{
		"filename":          "a001_c003.mov",
		"format":            "mov",
		"kind":              "video",
		"declared_checksum": checksum,
		"bytes":             1 << 24,
		"duration_ms":       120_000,
	}, nil)
	if asset.status != http.StatusCreated {
		h.t.Fatalf("ingest asset failed: %d %v", asset.status, asset.body)
	}
	assetID := h.stringField(asset, "id")

	verified := h.call(http.MethodPost, "/api/v1/assets/"+assetID+"/verify", editorToken, map[string]any{
		"observed_checksum": checksum,
	}, nil)
	if verified.status != http.StatusOK || verified.body["status"] != string(domain.AssetVerified) {
		h.t.Fatalf("verify asset failed: %d %v", verified.status, verified.body)
	}

	draft := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/timelines", editorToken, map[string]any{
		"notes": "offline cut",
	}, nil)
	if draft.status != http.StatusCreated {
		h.t.Fatalf("create draft failed: %d %v", draft.status, draft.body)
	}
	timelineID := h.stringField(draft, "id")

	for index, clip := range []struct {
		in, out int64
		track   string
	}{
		{0, 20_000, "program"},
		{20_000, 45_000, "program"},
		{0, 15_000, "audio"},
	} {
		added := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/clips", editorToken, map[string]any{
			"asset_id":      assetID,
			"order_index":   index,
			"source_in_ms":  clip.in,
			"source_out_ms": clip.out,
			"track":         clip.track,
			"speed_percent": 100,
		}, nil)
		if added.status != http.StatusCreated {
			h.t.Fatalf("add clip %d failed: %d %v", index, added.status, added.body)
		}
	}

	sealed := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/seal", editorToken, nil, nil)
	if sealed.status != http.StatusOK || sealed.body["status"] != string(domain.TimelineSealed) {
		h.t.Fatalf("seal failed: %d %v", sealed.status, sealed.body)
	}
	return projectID, timelineID
}

func TestCutReachesDeliveryEndToEnd(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REEL01")

	target := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", supervisorToken, map[string]any{
		"name":           "broadcast",
		"kind":           "webhook",
		"endpoint":       "https://broadcast.invalid/ingest",
		"credential_ref": "vault://broadcast",
	}, nil)
	if target.status != http.StatusCreated {
		t.Fatalf("create target failed: %d %v", target.status, target.body)
	}

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "web_1080p",
		"priority":    50,
	}, map[string]string{"Idempotency-Key": "spring-batch-1"})
	if submitted.status != http.StatusAccepted {
		t.Fatalf("submit render failed: %d %v", submitted.status, submitted.body)
	}
	jobID := h.stringField(submitted, "id")

	replay := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "web_1080p",
		"priority":    50,
	}, map[string]string{"Idempotency-Key": "spring-batch-1"})
	if replay.status != http.StatusAccepted {
		t.Fatalf("replayed submit failed: %d %v", replay.status, replay.body)
	}
	if h.stringField(replay, "id") != jobID {
		t.Fatalf("replay created a second job: %v", replay.body)
	}

	processed, err := h.app.Workers.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("worker cycle: %v", err)
	}
	if !processed {
		t.Fatal("the worker did not pick up the queued render")
	}

	job := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if job.body["status"] != string(domain.RenderSucceeded) {
		t.Fatalf("expected a finished render, got %v", job.body)
	}
	if job.body["output_uri"] == "" {
		t.Fatalf("finished render must carry an output reference: %v", job.body)
	}

	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", editorToken, nil, nil)
	if capacity.body["busy"].(float64) != 0 {
		t.Fatalf("the seat must be released after completion: %v", capacity.body)
	}

	dispatch := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/deliveries/dispatch", editorToken, nil, nil)
	if dispatch.status != http.StatusOK {
		t.Fatalf("dispatch failed: %d %v", dispatch.status, dispatch.body)
	}
	if dispatch.body["confirmed"].(float64) != 1 {
		t.Fatalf("expected one confirmed delivery: %v", dispatch.body)
	}
	if len(h.transport.Sent) != 1 {
		t.Fatalf("expected exactly one outbound delivery, got %d", len(h.transport.Sent))
	}
	if h.transport.Sent[0].JobID != jobID {
		t.Fatalf("outbound payload carried the wrong job: %+v", h.transport.Sent[0])
	}

	project := h.call(http.MethodGet, "/api/v1/projects/"+projectID, editorToken, nil, nil)
	if project.body["status"] != string(domain.ProjectDelivered) {
		t.Fatalf("project must be marked delivered: %v", project.body)
	}

	events := h.call(http.MethodGet, "/api/v1/audit-events?object_kind=render_job&page_size=50", supervisorToken, nil, nil)
	if events.status != http.StatusOK {
		t.Fatalf("audit listing failed: %d %v", events.status, events.body)
	}
	if events.body["total"].(float64) < 3 {
		t.Fatalf("expected submit, assign and complete audit events: %v", events.body)
	}
}

func TestRenderSubmissionRequiresSealedTimeline(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	project := h.call(http.MethodPost, "/api/v1/projects", editorToken, map[string]any{
		"code":        "DRAFT1",
		"title":       "Unsealed",
		"frame_rate":  24,
		"resolution":  "1920x1080",
		"deadline_at": h.clk.Now().Add(48 * time.Hour).Format(time.RFC3339),
	}, nil)
	projectID := h.stringField(project, "id")
	draft := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/timelines", editorToken, nil, nil)
	timelineID := h.stringField(draft, "id")

	answer := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "web_1080p",
	}, nil)
	if answer.status != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 for an unsealed timeline, got %d %v", answer.status, answer.body)
	}
	envelope, ok := answer.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing: %v", answer.body)
	}
	if envelope["code"] != "precondition_failed" {
		t.Fatalf("unexpected error code: %v", envelope)
	}
	if envelope["request_id"] == "" {
		t.Fatalf("error envelope must carry the request id: %v", envelope)
	}
}

func TestUnknownPresetIsRejected(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	_, timelineID := h.sealedCut(editorToken, "REEL02")

	answer := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID,
		"preset":      "imax_8k",
	}, nil)
	if answer.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown preset, got %d %v", answer.status, answer.body)
	}
}

func TestSecondActiveRenderForSameTimelineConflicts(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	_, timelineID := h.sealedCut(editorToken, "REEL03")

	first := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "proxy_540p",
	}, nil)
	if first.status != http.StatusAccepted {
		t.Fatalf("first submit failed: %d %v", first.status, first.body)
	}
	second := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "web_1080p",
	}, nil)
	if second.status != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate queue work, got %d %v", second.status, second.body)
	}
}

func TestAuthenticationAndAuthorizationBoundaries(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	anonymous := h.call(http.MethodGet, "/api/v1/projects", "", nil, nil)
	if anonymous.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", anonymous.status)
	}
	if anonymous.header.Get("X-Request-Id") == "" {
		t.Fatal("every response must carry a request id header")
	}

	forbidden := h.call(http.MethodPost, "/api/v1/render-farm/slots", editorToken, map[string]any{
		"name": "rogue-01", "pool": "primary", "units": 2,
	}, nil)
	if forbidden.status != http.StatusForbidden {
		t.Fatalf("editors must not operate the farm, got %d %v", forbidden.status, forbidden.body)
	}

	bogus := h.call(http.MethodGet, "/api/v1/projects", "not-a-real-token", nil, nil)
	if bogus.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an unknown token, got %d", bogus.status)
	}

	signedOut := h.call(http.MethodDelete, "/api/v1/sessions/current", editorToken, nil, nil)
	if signedOut.status != http.StatusNoContent {
		t.Fatalf("sign out failed: %d %v", signedOut.status, signedOut.body)
	}
	afterSignOut := h.call(http.MethodGet, "/api/v1/projects", editorToken, nil, nil)
	if afterSignOut.status != http.StatusUnauthorized {
		t.Fatalf("a revoked session must be rejected, got %d", afterSignOut.status)
	}
}

func TestEditorCannotReachAnotherEditorsProject(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	ownerToken := h.provisionEditor(supervisorToken)
	projectID, _ := h.sealedCut(ownerToken, "REEL04")

	intruder := h.call(http.MethodPost, "/api/v1/users", supervisorToken, map[string]string{
		"email":        "intruder@cutvideo.test",
		"display_name": "Other Editor",
		"role":         string(domain.RoleEditor),
		"password":     "intruder-secret-9",
	}, nil)
	if intruder.status != http.StatusCreated {
		t.Fatalf("provision second editor failed: %d %v", intruder.status, intruder.body)
	}
	intruderToken := h.signIn("intruder@cutvideo.test", "intruder-secret-9")

	answer := h.call(http.MethodGet, "/api/v1/projects/"+projectID, intruderToken, nil, nil)
	if answer.status != http.StatusForbidden {
		t.Fatalf("expected 403 across editors, got %d %v", answer.status, answer.body)
	}

	listing := h.call(http.MethodGet, "/api/v1/projects", intruderToken, nil, nil)
	if listing.body["total"].(float64) != 0 {
		t.Fatalf("an editor must only see their own projects: %v", listing.body)
	}
}

func TestBatchVerificationReportsPartialFailure(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	project := h.call(http.MethodPost, "/api/v1/projects", editorToken, map[string]any{
		"code":        "BATCH1",
		"title":       "Batch",
		"frame_rate":  25,
		"resolution":  "1920x1080",
		"deadline_at": h.clk.Now().Add(48 * time.Hour).Format(time.RFC3339),
	}, nil)
	projectID := h.stringField(project, "id")

	good := strings.Repeat("1", 64)
	bad := strings.Repeat("2", 64)
	goodID := ""
	badID := ""
	for _, entry := range []struct {
		name string
		sum  string
	}{{"good.mov", good}, {"bad.mov", bad}} {
		created := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/assets", editorToken, map[string]any{
			"filename":          entry.name,
			"format":            "mov",
			"kind":              "video",
			"declared_checksum": entry.sum,
			"bytes":             4096,
			"duration_ms":       10_000,
		}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("ingest %s failed: %d %v", entry.name, created.status, created.body)
		}
		if entry.sum == good {
			goodID = h.stringField(created, "id")
		} else {
			badID = h.stringField(created, "id")
		}
	}

	answer := h.call(http.MethodPost, "/api/v1/assets/verify-batch", editorToken, map[string]any{
		"items": []map[string]string{
			{"asset_id": goodID, "observed_checksum": good},
			{"asset_id": badID, "observed_checksum": strings.Repeat("9", 64)},
		},
	}, nil)
	if answer.status != http.StatusMultiStatus {
		t.Fatalf("expected 207 for a partially failed batch, got %d %v", answer.status, answer.body)
	}
	if answer.body["verified"].(float64) != 1 || answer.body["rejected"].(float64) != 1 {
		t.Fatalf("unexpected batch summary: %v", answer.body)
	}
	rejected := h.call(http.MethodGet, "/api/v1/assets/"+badID, editorToken, nil, nil)
	if rejected.body["status"] != string(domain.AssetRejected) {
		t.Fatalf("the mismatched asset must be rejected: %v", rejected.body)
	}
	verified := h.call(http.MethodGet, "/api/v1/assets/"+goodID, editorToken, nil, nil)
	if verified.body["status"] != string(domain.AssetVerified) {
		t.Fatalf("the clean asset must stay verified: %v", verified.body)
	}
}

func TestWorkerRetriesThenRecordsPermanentFailure(t *testing.T) {
	failing := worker.RendererFunc(func(_ context.Context, job *domain.RenderJob) (worker.Output, error) {
		return worker.Output{}, errors.New("encoder segfaulted")
	})
	h := newHarness(t, harnessOptions{renderer: failing, maxTries: 2, backoff: time.Second})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	_, timelineID := h.sealedCut(editorToken, "REEL05")

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "master_2160p",
	}, nil)
	jobID := h.stringField(submitted, "id")

	if _, err := h.app.Workers.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	afterFirst := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if afterFirst.body["status"] != string(domain.RenderQueued) {
		t.Fatalf("first failure must requeue the job: %v", afterFirst.body)
	}
	if afterFirst.body["attempt"].(float64) != 1 {
		t.Fatalf("expected one consumed attempt: %v", afterFirst.body)
	}

	// Inside the backoff window nothing may be claimed.
	if processed, err := h.app.Workers.ProcessOnce(context.Background()); err != nil || processed {
		t.Fatalf("backoff must hold the job back: processed=%v err=%v", processed, err)
	}
	h.clk.Advance(5 * time.Second)
	if _, err := h.app.Workers.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	afterSecond := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if afterSecond.body["status"] != string(domain.RenderFailed) {
		t.Fatalf("exhausted attempts must fail permanently: %v", afterSecond.body)
	}
	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", editorToken, nil, nil)
	if capacity.body["idle"].(float64) != 2 {
		t.Fatalf("both seats must be free after a permanent failure: %v", capacity.body)
	}
}

func TestWorkerShutdownReturnsJobToTheQueue(t *testing.T) {
	canceling := worker.RendererFunc(func(ctx context.Context, _ *domain.RenderJob) (worker.Output, error) {
		return worker.Output{}, context.Canceled
	})
	h := newHarness(t, harnessOptions{renderer: canceling})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	_, timelineID := h.sealedCut(editorToken, "REEL06")

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "proxy_540p",
	}, nil)
	jobID := h.stringField(submitted, "id")

	if _, err := h.app.Workers.ProcessOnce(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancellation to surface, got %v", err)
	}
	job := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if job.body["status"] != string(domain.RenderQueued) {
		t.Fatalf("an interrupted render must return to the queue: %v", job.body)
	}
	if job.body["slot_id"] != nil && job.body["slot_id"] != "" {
		t.Fatalf("an interrupted render must release its seat: %v", job.body)
	}
	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", editorToken, nil, nil)
	if capacity.body["busy"].(float64) != 0 {
		t.Fatalf("no seat may stay busy after shutdown: %v", capacity.body)
	}
}

func TestReaperRecoversExpiredLease(t *testing.T) {
	h := newHarness(t, harnessOptions{leaseTTL: 30 * time.Second})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	_, timelineID := h.sealedCut(editorToken, "REEL07")

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "web_1080p",
	}, nil)
	jobID := h.stringField(submitted, "id")

	claimed, err := h.app.Render.Claim(context.Background(), "primary")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected a claimed job")
	}
	h.clk.Advance(2 * time.Minute)

	report := h.app.Reaper.RunOnce(context.Background())
	if report.ObservedFailure != nil {
		t.Fatalf("housekeeping failed: %v", report.ObservedFailure)
	}
	if report.RequeuedJobs != 1 {
		t.Fatalf("expected one recovered job, got %d", report.RequeuedJobs)
	}
	job := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if job.body["status"] != string(domain.RenderQueued) {
		t.Fatalf("an abandoned lease must be requeued: %v", job.body)
	}
	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", editorToken, nil, nil)
	if capacity.body["busy"].(float64) != 0 {
		t.Fatalf("the abandoned seat must be freed: %v", capacity.body)
	}
}

// A long encode must keep the seat it already owns: while the renderer works the
// worker pushes the lease deadline forward, so housekeeping does not mistake
// running work for an abandoned seat.
func TestWorkerRefreshesLeaseWhileEncoding(t *testing.T) {
	var current *harness
	renderer := worker.RendererFunc(func(ctx context.Context, job *domain.RenderJob) (worker.Output, error) {
		// Business time moves past the deadline granted at assignment time.
		current.clk.Advance(90 * time.Second)
		giveUp := time.Now().Add(5 * time.Second)
		for {
			stored, err := current.app.Store.Renders().GetByID(ctx, job.ID)
			if err != nil {
				return worker.Output{}, err
			}
			if stored.LeaseExpiresAt != nil && stored.LeaseExpiresAt.After(current.clk.Now()) {
				break
			}
			if time.Now().After(giveUp) {
				return worker.Output{}, fmt.Errorf("the lease of %s was never refreshed", job.ID)
			}
			time.Sleep(2 * time.Millisecond)
		}
		return worker.Output{URI: "cutvideo://renders/" + job.ID + ".mov", Bytes: 6 << 20}, nil
	})

	h := newHarness(t, harnessOptions{
		leaseTTL:   time.Minute,
		leaseRenew: 2 * time.Millisecond,
		renderer:   renderer,
	})
	current = h
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	_, timelineID := h.sealedCut(editorToken, "REEL14")

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "master_2160p",
	}, nil)
	if submitted.status != http.StatusAccepted {
		t.Fatalf("submit render failed: %d %v", submitted.status, submitted.body)
	}
	jobID := h.stringField(submitted, "id")

	processed, err := h.app.Workers.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("worker cycle: %v", err)
	}
	if !processed {
		t.Fatal("the worker did not pick up the queued render")
	}

	report := h.app.Reaper.RunOnce(context.Background())
	if report.ObservedFailure != nil {
		t.Fatalf("housekeeping failed: %v", report.ObservedFailure)
	}
	if report.RequeuedJobs != 0 {
		t.Fatalf("a finished render must not be requeued, got %d", report.RequeuedJobs)
	}
	job := h.call(http.MethodGet, "/api/v1/renders/"+jobID, editorToken, nil, nil)
	if job.body["status"] != string(domain.RenderSucceeded) {
		t.Fatalf("the long render must finish on its original seat: %v", job.body)
	}
	if job.body["attempt"].(float64) != 1 {
		t.Fatalf("a renewed lease must not burn a second attempt: %v", job.body)
	}
}

func TestConcurrentClaimsNeverDoubleBookASeat(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	for index, code := range []string{"RACE01", "RACE02", "RACE03", "RACE04"} {
		_, timelineID := h.sealedCut(editorToken, code)
		submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
			"timeline_id": timelineID,
			"preset":      "proxy_540p",
			"priority":    50,
		}, nil)
		if submitted.status != http.StatusAccepted {
			t.Fatalf("submit %d failed: %d %v", index, submitted.status, submitted.body)
		}
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed []string
		start   = make(chan struct{})
	)
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			job, err := h.app.Render.Claim(context.Background(), "primary")
			if err != nil || job == nil {
				return
			}
			mu.Lock()
			claimed = append(claimed, job.SlotID)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if len(claimed) != 2 {
		t.Fatalf("only the two provisioned seats may be claimed, got %d", len(claimed))
	}
	if claimed[0] == claimed[1] {
		t.Fatalf("two jobs claimed the same seat %q", claimed[0])
	}
	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", editorToken, nil, nil)
	if capacity.body["busy"].(float64) != 2 || capacity.body["idle"].(float64) != 0 {
		t.Fatalf("unexpected farm capacity: %v", capacity.body)
	}
}

func TestCancelReleasesSeatAndBlocksLateCancel(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	_, timelineID := h.sealedCut(editorToken, "REEL08")

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "web_1080p",
	}, nil)
	jobID := h.stringField(submitted, "id")
	if _, err := h.app.Render.Claim(context.Background(), "primary"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	canceled := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/cancel", editorToken, map[string]any{
		"reason": "client pulled the cut",
	}, nil)
	if canceled.status != http.StatusOK || canceled.body["status"] != string(domain.RenderCanceled) {
		t.Fatalf("cancel failed: %d %v", canceled.status, canceled.body)
	}
	capacity := h.call(http.MethodGet, "/api/v1/render-farm/capacity", editorToken, nil, nil)
	if capacity.body["busy"].(float64) != 0 {
		t.Fatalf("cancel must free the seat: %v", capacity.body)
	}
	again := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/cancel", editorToken, map[string]any{
		"reason": "again",
	}, nil)
	if again.status != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 when cancelling a finished job, got %d %v", again.status, again.body)
	}
}

func TestDeliveryDispatchReportsPartialFailureAndSkipsDisabledTargets(t *testing.T) {
	transport := &delivery.StubTransport{Failures: map[string]error{
		"archive": errors.New("aspera endpoint refused the transfer"),
	}}
	h := newHarness(t, harnessOptions{transport: transport})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REEL09")

	for _, entry := range []struct {
		name string
		kind string
	}{{"broadcast", "webhook"}, {"archive", "aspera"}, {"social", "s3"}} {
		created := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", supervisorToken, map[string]any{
			"name":           entry.name,
			"kind":           entry.kind,
			"endpoint":       "https://" + entry.name + ".invalid/ingest",
			"credential_ref": "vault://" + entry.name,
		}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("create target %s failed: %d %v", entry.name, created.status, created.body)
		}
		if entry.name == "social" {
			disabled := h.call(http.MethodPatch, "/api/v1/delivery-targets/"+h.stringField(created, "id"),
				supervisorToken, map[string]any{"enabled": false}, nil)
			if disabled.status != http.StatusOK {
				t.Fatalf("disable target failed: %d %v", disabled.status, disabled.body)
			}
		}
	}

	submitted := h.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "web_1080p",
	}, nil)
	jobID := h.stringField(submitted, "id")
	if _, err := h.app.Workers.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("worker cycle: %v", err)
	}

	dispatch := h.call(http.MethodPost, "/api/v1/renders/"+jobID+"/deliveries/dispatch", editorToken, nil, nil)
	if dispatch.status != http.StatusMultiStatus {
		t.Fatalf("expected 207 for a partial delivery, got %d %v", dispatch.status, dispatch.body)
	}
	if dispatch.body["confirmed"].(float64) != 1 {
		t.Fatalf("the reachable destination must confirm: %v", dispatch.body)
	}
	if dispatch.body["failed"].(float64) != 1 {
		t.Fatalf("the refusing destination must be reported as failed: %v", dispatch.body)
	}

	records := h.call(http.MethodGet, "/api/v1/renders/"+jobID+"/deliveries", editorToken, nil, nil)
	items, ok := records.body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("only enabled destinations get delivery records: %v", records.body)
	}

	project := h.call(http.MethodGet, "/api/v1/projects/"+projectID, editorToken, nil, nil)
	if project.body["status"] == string(domain.ProjectDelivered) {
		t.Fatalf("a partially delivered project must not be marked delivered: %v", project.body)
	}
}

func TestDeliveryTargetManagementIsSupervisorOnly(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, _ := h.sealedCut(editorToken, "REEL20")

	created := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", supervisorToken, map[string]any{
		"name":           "broadcast",
		"kind":           "webhook",
		"endpoint":       "https://broadcast.invalid/ingest",
		"credential_ref": "vault://broadcast",
	}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("supervisor must create a destination: %d %v", created.status, created.body)
	}
	targetID := h.stringField(created, "id")

	editorCreate := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/delivery-targets", editorToken, map[string]any{
		"name":           "rogue",
		"kind":           "webhook",
		"endpoint":       "https://rogue.invalid/ingest",
		"credential_ref": "vault://rogue",
	}, nil)
	if editorCreate.status != http.StatusForbidden {
		t.Fatalf("an editor must not create a destination, got %d %v", editorCreate.status, editorCreate.body)
	}

	editorDisable := h.call(http.MethodPatch, "/api/v1/delivery-targets/"+targetID, editorToken,
		map[string]any{"enabled": false}, nil)
	if editorDisable.status != http.StatusForbidden {
		t.Fatalf("an editor must not disable a destination, got %d %v", editorDisable.status, editorDisable.body)
	}

	supervisorDisable := h.call(http.MethodPatch, "/api/v1/delivery-targets/"+targetID, supervisorToken,
		map[string]any{"enabled": false}, nil)
	if supervisorDisable.status != http.StatusOK {
		t.Fatalf("supervisor must disable a destination: %d %v", supervisorDisable.status, supervisorDisable.body)
	}

	listed := h.call(http.MethodGet, "/api/v1/projects/"+projectID+"/delivery-targets", editorToken, nil, nil)
	if listed.status != http.StatusOK {
		t.Fatalf("an editor must still read the destination list: %d %v", listed.status, listed.body)
	}
}

func TestHTTPTransportPropagatesDownstreamRejection(t *testing.T) {
	var received int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		if r.Header.Get("X-CutVideo-Target") == "" {
			t.Errorf("target header missing")
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream busy"))
	}))
	defer server.Close()

	transport := delivery.NewHTTPTransport(server.Client(), 2*time.Second)
	target, err := domain.NewDeliveryTarget("dst_1", "prj_1", "broadcast", domain.DeliveryWebhook,
		server.URL, "vault://cred", time.Now())
	if err != nil {
		t.Fatalf("new target: %v", err)
	}
	answer, err := transport.Send(context.Background(), target, delivery.Payload{JobID: "rnd_1"})
	if !errors.Is(err, delivery.ErrRejectedDownstream) {
		t.Fatalf("expected a downstream rejection, got %v", err)
	}
	if answer != "upstream busy" {
		t.Fatalf("the response body must be preserved for the audit trail, got %q", answer)
	}
	if received != 1 {
		t.Fatalf("expected exactly one outbound call, got %d", received)
	}
}

func TestPaginationContractOnProjectListing(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	for index := 0; index < 6; index++ {
		created := h.call(http.MethodPost, "/api/v1/projects", editorToken, map[string]any{
			"code":        fmt.Sprintf("PAGE%02d", index),
			"title":       fmt.Sprintf("Cut %02d", index),
			"frame_rate":  25,
			"resolution":  "1920x1080",
			"deadline_at": h.clk.Now().Add(72 * time.Hour).Format(time.RFC3339),
		}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("create project %d failed: %d %v", index, created.status, created.body)
		}
	}

	first := h.call(http.MethodGet, "/api/v1/projects?page=1&page_size=4&sort_by=code&sort=asc", editorToken, nil, nil)
	if first.body["total"].(float64) != 6 || first.body["total_pages"].(float64) != 2 {
		t.Fatalf("unexpected pagination envelope: %v", first.body)
	}
	items := first.body["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	if items[0].(map[string]any)["code"] != "PAGE00" {
		t.Fatalf("ascending order broken: %v", items[0])
	}

	invalid := h.call(http.MethodGet, "/api/v1/projects?page_size=5000", editorToken, nil, nil)
	if invalid.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an oversized page, got %d %v", invalid.status, invalid.body)
	}
}

func TestReadinessReflectsFarmAndStore(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	answer := h.call(http.MethodGet, "/readyz", "", nil, nil)
	if answer.status != http.StatusOK {
		t.Fatalf("expected the service to be ready, got %d %v", answer.status, answer.body)
	}
	checks := answer.body["checks"].(map[string]any)
	if checks["database"] != "ok" || checks["render_farm"] != "ok" {
		t.Fatalf("unexpected readiness checks: %v", checks)
	}
	live := h.call(http.MethodGet, "/healthz", "", nil, nil)
	if live.status != http.StatusOK || live.body["status"] != "alive" {
		t.Fatalf("liveness probe failed: %d %v", live.status, live.body)
	}
	missing := h.call(http.MethodGet, "/api/v1/nothing-here", "", nil, nil)
	if missing.status != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown route, got %d", missing.status)
	}
}

func TestSealedTimelineSurvivesProcessRestart(t *testing.T) {
	dir := t.TempDir()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)",
		filepath.ToSlash(filepath.Join(dir, "restart-app.db")))

	first := newHarness(t, harnessOptions{dsn: dsn, randomIDs: true})
	supervisorToken := first.signIn(supervisorEmail, supervisorPassword)
	editorToken := first.provisionEditor(supervisorToken)
	projectID, timelineID := first.sealedCut(editorToken, "REEL10")
	submitted := first.call(http.MethodPost, "/api/v1/renders", editorToken, map[string]any{
		"timeline_id": timelineID, "preset": "web_1080p",
	}, nil)
	jobID := first.stringField(submitted, "id")
	first.server.Close()
	if err := first.app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	second := newHarness(t, harnessOptions{dsn: dsn, randomIDs: true})
	reopenedToken := second.signIn(editorEmail, editorPassword)
	project := second.call(http.MethodGet, "/api/v1/projects/"+projectID, reopenedToken, nil, nil)
	if project.status != http.StatusOK || project.body["sealed_version"].(float64) != 1 {
		t.Fatalf("sealed state must survive a restart: %d %v", project.status, project.body)
	}
	if _, err := second.app.Workers.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("worker cycle after restart: %v", err)
	}
	job := second.call(http.MethodGet, "/api/v1/renders/"+jobID, reopenedToken, nil, nil)
	if job.body["status"] != string(domain.RenderSucceeded) {
		t.Fatalf("queued work must resume after a restart: %v", job.body)
	}
}

func TestClipsCannotBeAddedToSealedTimeline(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)
	projectID, timelineID := h.sealedCut(editorToken, "REEL11")

	assets := h.call(http.MethodGet, "/api/v1/projects/"+projectID+"/assets", editorToken, nil, nil)
	items := assets.body["items"].([]any)
	assetID := items[0].(map[string]any)["id"].(string)

	answer := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/clips", editorToken, map[string]any{
		"asset_id":      assetID,
		"order_index":   9,
		"source_in_ms":  0,
		"source_out_ms": 3_000,
		"track":         "program",
		"speed_percent": 100,
	}, nil)
	if answer.status != http.StatusPreconditionFailed {
		t.Fatalf("a sealed timeline must reject new clips, got %d %v", answer.status, answer.body)
	}
}

func TestQuarantinedFootageBlocksSealing(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)
	editorToken := h.provisionEditor(supervisorToken)

	project := h.call(http.MethodPost, "/api/v1/projects", editorToken, map[string]any{
		"code":        "QUAR01",
		"title":       "Quarantine",
		"frame_rate":  25,
		"resolution":  "1920x1080",
		"deadline_at": h.clk.Now().Add(48 * time.Hour).Format(time.RFC3339),
	}, nil)
	projectID := h.stringField(project, "id")
	checksum := strings.Repeat("7", 64)
	asset := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/assets", editorToken, map[string]any{
		"filename":          "hold.mov",
		"format":            "mov",
		"kind":              "video",
		"declared_checksum": checksum,
		"bytes":             8192,
		"duration_ms":       20_000,
	}, nil)
	assetID := h.stringField(asset, "id")
	if verified := h.call(http.MethodPost, "/api/v1/assets/"+assetID+"/verify", editorToken, map[string]any{
		"observed_checksum": checksum,
	}, nil); verified.status != http.StatusOK {
		t.Fatalf("verify failed: %d %v", verified.status, verified.body)
	}
	draft := h.call(http.MethodPost, "/api/v1/projects/"+projectID+"/timelines", editorToken, nil, nil)
	timelineID := h.stringField(draft, "id")
	if added := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/clips", editorToken, map[string]any{
		"asset_id":      assetID,
		"order_index":   0,
		"source_in_ms":  0,
		"source_out_ms": 10_000,
		"track":         "program",
		"speed_percent": 100,
	}, nil); added.status != http.StatusCreated {
		t.Fatalf("add clip failed: %d %v", added.status, added.body)
	}
	if quarantined := h.call(http.MethodPost, "/api/v1/assets/"+assetID+"/quarantine", editorToken, map[string]any{
		"reason": "legal hold on the location footage",
	}, nil); quarantined.status != http.StatusOK {
		t.Fatalf("quarantine failed: %d %v", quarantined.status, quarantined.body)
	}

	sealed := h.call(http.MethodPost, "/api/v1/timelines/"+timelineID+"/seal", editorToken, nil, nil)
	if sealed.status != http.StatusPreconditionFailed {
		t.Fatalf("sealing with quarantined footage must fail, got %d %v", sealed.status, sealed.body)
	}
	timeline := h.call(http.MethodGet, "/api/v1/timelines/"+timelineID, editorToken, nil, nil)
	if timeline.body["status"] != string(domain.TimelineDraft) {
		t.Fatalf("the failed seal must leave the draft untouched: %v", timeline.body)
	}
	projectAfter := h.call(http.MethodGet, "/api/v1/projects/"+projectID, editorToken, nil, nil)
	if projectAfter.body["sealed_version"].(float64) != 0 {
		t.Fatalf("a failed seal must not advance the project pointer: %v", projectAfter.body)
	}
}
