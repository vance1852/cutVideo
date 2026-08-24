package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

func reference() time.Time {
	return time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
}

func newTestAsset(t *testing.T, id string, durationMS int64) *domain.MediaAsset {
	t.Helper()
	asset, err := domain.NewMediaAsset(id, "prj_1", id+".mov", "mov", domain.AssetKindVideo,
		strings.Repeat("a", 64), 1<<20, durationMS, reference(), 48*time.Hour)
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}
	if err := asset.Verify(strings.Repeat("a", 64), reference()); err != nil {
		t.Fatalf("verify asset: %v", err)
	}
	return asset
}

func TestUserRolesCarryDistinctAuthority(t *testing.T) {
	editor := domain.Principal{UserID: "u1", Role: domain.RoleEditor}
	supervisor := domain.Principal{UserID: "u2", Role: domain.RoleSupervisor}
	auditor := domain.Principal{UserID: "u3", Role: domain.RoleAuditor}

	if err := editor.RequireEdit(); err != nil {
		t.Fatalf("editor should be able to edit: %v", err)
	}
	if err := editor.RequireFarmArbitration(); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("editor must not arbitrate the farm, got %v", err)
	}
	if err := supervisor.RequireDeliveryManagement(); err != nil {
		t.Fatalf("supervisor should manage delivery: %v", err)
	}
	if err := auditor.RequireEdit(); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("auditor must not edit, got %v", err)
	}
}

func TestNewUserRejectsUnknownRoleAndBadEmail(t *testing.T) {
	if _, err := domain.NewUser("u1", "editor@example.com", "Editor", domain.Role("colorist"), "hash", reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown role must be rejected, got %v", err)
	}
	if _, err := domain.NewUser("u1", "not-an-email", "Editor", domain.RoleEditor, "hash", reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("invalid email must be rejected, got %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	session, err := domain.NewSession("ses_1", "usr_1", "hash", "agent", reference(), time.Hour)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if err := session.EnsureUsable(reference().Add(time.Minute)); err != nil {
		t.Fatalf("fresh session must be usable: %v", err)
	}
	if err := session.EnsureUsable(reference().Add(2 * time.Hour)); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("expected expiry, got %v", err)
	}
	if remaining := session.RemainingTTL(reference().Add(30 * time.Minute)); remaining != 30*time.Minute {
		t.Fatalf("unexpected remaining ttl %s", remaining)
	}
	if err := session.Revoke(reference()); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := session.EnsureUsable(reference()); !errors.Is(err, domain.ErrSessionRevoked) {
		t.Fatalf("expected revoked, got %v", err)
	}
	if err := session.Revoke(reference()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("double revoke must be rejected, got %v", err)
	}
}

func TestAssetVerificationRejectsChecksumMismatch(t *testing.T) {
	asset, err := domain.NewMediaAsset("ast_1", "prj_1", "a001.mov", "mov", domain.AssetKindVideo,
		strings.Repeat("b", 64), 4096, 10_000, reference(), time.Hour)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	if err := asset.Verify(strings.Repeat("c", 64), reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("mismatch must be rejected, got %v", err)
	}
	if asset.Status != domain.AssetRejected {
		t.Fatalf("expected rejected status, got %s", asset.Status)
	}
	if err := asset.Usable(reference()); !errors.Is(err, domain.ErrAssetUnusable) {
		t.Fatalf("rejected asset must be unusable, got %v", err)
	}
}

func TestAssetRetentionGate(t *testing.T) {
	asset := newTestAsset(t, "ast_2", 20_000)
	if err := asset.Usable(reference()); err != nil {
		t.Fatalf("verified asset must be usable: %v", err)
	}
	if err := asset.Usable(reference().Add(49 * time.Hour)); !errors.Is(err, domain.ErrRetentionExpired) {
		t.Fatalf("expected retention expiry, got %v", err)
	}
	if err := asset.ExtendRetention(72*time.Hour, reference()); err != nil {
		t.Fatalf("extend retention: %v", err)
	}
	if err := asset.Usable(reference().Add(49 * time.Hour)); err != nil {
		t.Fatalf("extended asset must be usable: %v", err)
	}
}

func TestAssetCloneIsolatesVerifiedTimestamp(t *testing.T) {
	asset := newTestAsset(t, "ast_3", 5_000)
	clone := asset.Clone()
	*clone.VerifiedAt = clone.VerifiedAt.Add(72 * time.Hour)
	if asset.VerifiedAt.Equal(*clone.VerifiedAt) {
		t.Fatal("clone must not share the verified timestamp pointer")
	}
}

func TestProjectLockRequiresSealedVersion(t *testing.T) {
	project, err := domain.NewProject("prj_1", "reel", "Spring Reel", "usr_1", 25, "1920x1080",
		reference().Add(72*time.Hour), reference())
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	if project.Code != "REEL" {
		t.Fatalf("code must be normalized, got %s", project.Code)
	}
	if err := project.Lock(reference()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("locking without a sealed version must fail, got %v", err)
	}
	if err := project.RecordDraft(1, reference()); err != nil {
		t.Fatalf("record draft: %v", err)
	}
	if err := project.RecordSeal(1, reference()); err != nil {
		t.Fatalf("record seal: %v", err)
	}
	if err := project.Lock(reference()); err != nil {
		t.Fatalf("lock after seal: %v", err)
	}
	if err := project.EnsureEditable(); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("locked project must not be editable, got %v", err)
	}
}

func TestProjectVersionBookkeeping(t *testing.T) {
	project, err := domain.NewProject("prj_2", "CUT01", "Cut", "usr_1", 24, "3840x2160",
		reference().Add(24*time.Hour), reference())
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	if err := project.RecordDraft(2, reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("non sequential draft must be rejected, got %v", err)
	}
	if err := project.RecordDraft(1, reference()); err != nil {
		t.Fatalf("first draft: %v", err)
	}
	if err := project.RecordSeal(2, reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("sealing an undrafted version must be rejected, got %v", err)
	}
}

func TestTimelineSealChecksClipsAndAssets(t *testing.T) {
	version, err := domain.NewTimelineVersion("tml_1", "prj_1", 1, "usr_1", "first pass", reference())
	if err != nil {
		t.Fatalf("new version: %v", err)
	}
	if err := version.Seal(map[string]*domain.MediaAsset{}, reference()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("empty version must not seal, got %v", err)
	}

	asset := newTestAsset(t, "ast_10", 30_000)
	clip, err := domain.NewClip("clp_1", version.ID, asset.ID, 0, 0, 12_000, domain.TrackProgram, "cut", 100, reference())
	if err != nil {
		t.Fatalf("new clip: %v", err)
	}
	if err := version.AddClip(clip, reference()); err != nil {
		t.Fatalf("add clip: %v", err)
	}
	duplicate, err := domain.NewClip("clp_2", version.ID, asset.ID, 0, 0, 4_000, domain.TrackProgram, "", 100, reference())
	if err != nil {
		t.Fatalf("new clip: %v", err)
	}
	if err := version.AddClip(duplicate, reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("duplicate order slot must be rejected, got %v", err)
	}

	assets := map[string]*domain.MediaAsset{asset.ID: asset}
	if err := version.Seal(assets, reference()); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if version.Status != domain.TimelineSealed {
		t.Fatalf("expected sealed, got %s", version.Status)
	}
	if version.TotalDurationMS != 12_000 {
		t.Fatalf("expected 12000ms program duration, got %d", version.TotalDurationMS)
	}
	if err := version.EnsureEditable(); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("sealed version must reject edits, got %v", err)
	}
	if err := version.Renderable(); err != nil {
		t.Fatalf("sealed version must be renderable: %v", err)
	}
}

func TestTimelineSealRejectsQuarantinedFootage(t *testing.T) {
	version, _ := domain.NewTimelineVersion("tml_2", "prj_1", 1, "usr_1", "", reference())
	asset := newTestAsset(t, "ast_11", 20_000)
	clip, _ := domain.NewClip("clp_3", version.ID, asset.ID, 0, 0, 8_000, domain.TrackProgram, "", 100, reference())
	if err := version.AddClip(clip, reference()); err != nil {
		t.Fatalf("add clip: %v", err)
	}
	if err := asset.Quarantine("legal hold", reference()); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	err := version.Seal(map[string]*domain.MediaAsset{asset.ID: asset}, reference())
	if !errors.Is(err, domain.ErrAssetUnusable) {
		t.Fatalf("expected unusable footage error, got %v", err)
	}
	if version.Status != domain.TimelineDraft {
		t.Fatalf("failed seal must leave the version in draft, got %s", version.Status)
	}
}

func TestAssetUsableGatesQuarantinedFootage(t *testing.T) {
	asset := newTestAsset(t, "ast_q", 20_000)
	if err := asset.Usable(reference()); err != nil {
		t.Fatalf("verified asset must be usable: %v", err)
	}
	if err := asset.Quarantine("audio drift", reference()); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if err := asset.Usable(reference()); !errors.Is(err, domain.ErrAssetUnusable) {
		t.Fatalf("quarantined asset must be unusable, got %v", err)
	}
}

func TestAssetUsableGatesArchivedFootage(t *testing.T) {
	asset := newTestAsset(t, "ast_a", 20_000)
	if err := asset.Archive(reference()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := asset.Usable(reference()); !errors.Is(err, domain.ErrAssetUnusable) {
		t.Fatalf("archived asset must be unusable, got %v", err)
	}
}

func TestAssetUsableGatesIngestingFootage(t *testing.T) {
	asset, err := domain.NewMediaAsset("ast_i", "prj_1", "raw.mov", "mov", domain.AssetKindVideo,
		strings.Repeat("z", 64), 4096, 10_000, reference(), 48*time.Hour)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	if asset.Status != domain.AssetIngesting {
		t.Fatalf("expected ingesting, got %s", asset.Status)
	}
	if err := asset.Usable(reference()); !errors.Is(err, domain.ErrAssetUnusable) {
		t.Fatalf("ingesting asset must be unusable, got %v", err)
	}
}

func TestTimelineSpeedRampChangesProgramDuration(t *testing.T) {
	version, _ := domain.NewTimelineVersion("tml_3", "prj_1", 1, "usr_1", "", reference())
	asset := newTestAsset(t, "ast_12", 60_000)
	slow, _ := domain.NewClip("clp_4", version.ID, asset.ID, 0, 0, 10_000, domain.TrackProgram, "", 50, reference())
	if err := version.AddClip(slow, reference()); err != nil {
		t.Fatalf("add clip: %v", err)
	}
	if got := version.ProgramDurationMS(); got != 20_000 {
		t.Fatalf("a 50 percent ramp must double the program duration, got %d", got)
	}
}

func TestTimelineClipsAreCopies(t *testing.T) {
	version, _ := domain.NewTimelineVersion("tml_4", "prj_1", 1, "usr_1", "", reference())
	asset := newTestAsset(t, "ast_13", 20_000)
	clip, _ := domain.NewClip("clp_5", version.ID, asset.ID, 0, 0, 5_000, domain.TrackProgram, "", 100, reference())
	if err := version.AddClip(clip, reference()); err != nil {
		t.Fatalf("add clip: %v", err)
	}
	exported := version.Clips()
	exported[0].SourceOutMS = 999_999
	if version.Clips()[0].SourceOutMS != 5_000 {
		t.Fatal("exported clips must not alias timeline state")
	}
}

func TestClipRangeMustFitAsset(t *testing.T) {
	asset := newTestAsset(t, "ast_14", 6_000)
	clip, err := domain.NewClip("clp_6", "tml_5", asset.ID, 0, 0, 9_000, domain.TrackProgram, "", 100, reference())
	if err != nil {
		t.Fatalf("new clip: %v", err)
	}
	if err := clip.FitsAsset(asset); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected range validation error, got %v", err)
	}
}

func TestRenderJobHappyPath(t *testing.T) {
	job, err := domain.NewRenderJob("rnd_1", "prj_1", "tml_1", "usr_1", "web_1080p", domain.PriorityNormal, 3, "key-1", reference())
	if err != nil {
		t.Fatalf("new job: %v", err)
	}
	if !job.Eligible(reference()) {
		t.Fatal("a fresh job must be eligible")
	}
	if err := job.Assign("slt_1", 2*time.Minute, reference()); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if job.Attempt != 1 {
		t.Fatalf("assignment must consume an attempt, got %d", job.Attempt)
	}
	if err := job.Start(reference().Add(time.Second)); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := job.Complete("cutvideo://out.mov", 4096, reference().Add(time.Minute)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if job.HoldsSlot() {
		t.Fatal("a completed job must release its seat")
	}
	if err := job.Complete("cutvideo://out.mov", 4096, reference()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("double completion must be rejected, got %v", err)
	}
}

func TestRenderJobRetriesThenFailsPermanently(t *testing.T) {
	job, _ := domain.NewRenderJob("rnd_2", "prj_1", "tml_1", "usr_1", "proxy_540p", domain.PriorityNormal, 2, "", reference())
	if err := job.Assign("slt_1", time.Minute, reference()); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := job.Start(reference()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := job.Fail("encoder crashed", 2*time.Second, reference()); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if job.Status != domain.RenderQueued {
		t.Fatalf("first failure must requeue, got %s", job.Status)
	}
	if !job.NextAttemptAt.After(reference()) {
		t.Fatal("requeued job must carry a backoff window")
	}
	if job.Eligible(reference()) {
		t.Fatal("job inside its backoff window must not be eligible")
	}
	later := job.NextAttemptAt
	if err := job.Assign("slt_1", time.Minute, later); err != nil {
		t.Fatalf("second assign: %v", err)
	}
	if err := job.Start(later); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if err := job.Fail("encoder crashed again", 2*time.Second, later); err != nil {
		t.Fatalf("second fail: %v", err)
	}
	if job.Status != domain.RenderFailed {
		t.Fatalf("exhausted attempts must fail permanently, got %s", job.Status)
	}
	if job.FinishedAt == nil {
		t.Fatal("a permanently failed job must record a finish time")
	}
}

func TestRenderJobCancelAuthority(t *testing.T) {
	job, _ := domain.NewRenderJob("rnd_3", "prj_1", "tml_1", "usr_owner", "web_1080p", domain.PriorityUrgent, 3, "", reference())
	other := domain.Principal{UserID: "usr_other", Role: domain.RoleEditor}
	if err := job.EnsureCancelAuthority(other); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("another editor must not cancel, got %v", err)
	}
	supervisor := domain.Principal{UserID: "usr_sup", Role: domain.RoleSupervisor}
	if err := job.EnsureCancelAuthority(supervisor); err != nil {
		t.Fatalf("supervisor must be able to cancel: %v", err)
	}
	if err := job.Cancel("client pulled the cut", reference()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := job.Cancel("again", reference()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("cancelling a finished job must fail, got %v", err)
	}
}

func TestRenderJobLeaseExpiry(t *testing.T) {
	job, _ := domain.NewRenderJob("rnd_4", "prj_1", "tml_1", "usr_1", "master_2160p", domain.PriorityNormal, 3, "", reference())
	if err := job.Assign("slt_2", time.Minute, reference()); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if job.LeaseExpired(reference().Add(30 * time.Second)) {
		t.Fatal("lease must still be valid")
	}
	if !job.LeaseExpired(reference().Add(2 * time.Minute)) {
		t.Fatal("lease must be reported as expired")
	}
	if err := job.Requeue("lease expired", time.Second, reference().Add(2*time.Minute)); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if job.HoldsSlot() || job.Status != domain.RenderQueued {
		t.Fatalf("requeue must release the seat and requeue, got %s slot=%q", job.Status, job.SlotID)
	}
	if job.Attempt != 1 {
		t.Fatalf("requeue must not consume an extra attempt, got %d", job.Attempt)
	}
}

func TestRenderSlotExclusiveOwnership(t *testing.T) {
	slot, err := domain.NewRenderSlot("slt_1", "primary-01", "primary", 4, reference())
	if err != nil {
		t.Fatalf("new slot: %v", err)
	}
	if err := slot.Reserve("rnd_1", reference()); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := slot.Reserve("rnd_2", reference()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("second reservation must be rejected, got %v", err)
	}
	if err := slot.Release("rnd_2", reference()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("releasing on behalf of another job must conflict, got %v", err)
	}
	if err := slot.Release("rnd_1", reference()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if !slot.Available() {
		t.Fatal("released slot must be available again")
	}
}

func TestRenderSlotDrainTakesSeatOffline(t *testing.T) {
	slot, _ := domain.NewRenderSlot("slt_2", "primary-02", "primary", 2, reference())
	if err := slot.Reserve("rnd_9", reference()); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := slot.Drain(reference()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if slot.Status != domain.SlotDraining {
		t.Fatalf("expected draining, got %s", slot.Status)
	}
}

func TestDeliveryRecordAttemptBudget(t *testing.T) {
	record, err := domain.NewDeliveryRecord("dlv_1", "rnd_1", "dst_1", "prj_1", "cutvideo://out.mov", 2, reference())
	if err != nil {
		t.Fatalf("new record: %v", err)
	}
	if err := record.MarkDispatched(reference()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := record.MarkFailed("connection reset", reference()); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := record.MarkDispatched(reference()); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	if !record.Exhausted() {
		t.Fatal("two attempts must exhaust the budget")
	}
	if err := record.Confirm(reference()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := record.MarkFailed("late failure", reference()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("confirmed delivery must not fail afterwards, got %v", err)
	}
}

func TestPageValidation(t *testing.T) {
	if _, err := domain.NewPage(1, 500, "created_at", domain.SortAscending); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("oversized page must be rejected, got %v", err)
	}
	if _, err := domain.NewPage(1, 10, "created_at", domain.SortDirection("sideways")); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown sort must be rejected, got %v", err)
	}
	page, err := domain.NewPage(3, 10, "created_at", domain.SortAscending)
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	if page.Offset() != 20 || page.Limit() != 10 || page.Descending() {
		t.Fatalf("unexpected page %+v", page)
	}
	result := domain.NewPageResult([]string{"a", "b"}, 23, page)
	if result.TotalPages != 3 {
		t.Fatalf("expected 3 total pages, got %d", result.TotalPages)
	}
}

func TestAuditEventValidation(t *testing.T) {
	if _, err := domain.NewAuditEvent("aud_1", "usr_1", domain.RoleEditor, "", "render_job", "rnd_1",
		domain.AuditSucceeded, "req_1", "", reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("empty action must be rejected, got %v", err)
	}
	if _, err := domain.NewAuditEvent("aud_1", "usr_1", domain.RoleEditor, "render.submit", "render_job", "rnd_1",
		domain.AuditResult("maybe"), "req_1", "", reference()); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown result must be rejected, got %v", err)
	}
}

func TestIdempotencyRecordMatching(t *testing.T) {
	record, err := domain.NewIdempotencyRecord("idem_1", "render.submit", "post", "/api/v1/renders",
		"usr_1", "key-1", "hash-1", 202, "{}", reference(), time.Hour)
	if err != nil {
		t.Fatalf("new record: %v", err)
	}
	if record.Method != "POST" {
		t.Fatalf("method must be normalized, got %s", record.Method)
	}
	if !record.Matches("hash-1") || record.Matches("hash-2") {
		t.Fatal("payload matching is incorrect")
	}
	if record.Expired(reference().Add(30 * time.Minute)) {
		t.Fatal("record must still be valid")
	}
	if !record.Expired(reference().Add(2 * time.Hour)) {
		t.Fatal("record must be expired")
	}
}
