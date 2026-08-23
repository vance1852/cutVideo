package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/storage/sqlite"
)

func dsnFor(dir, name string) string {
	path := filepath.ToSlash(filepath.Join(dir, name))
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
}

func openStore(t *testing.T, dsn string) *sqlite.DB {
	t.Helper()
	store, err := sqlite.Open(context.Background(), config.DatabaseConfig{
		DSN:           dsn,
		MaxOpenConns:  1,
		RunMigrations: true,
	}, logging.Discard())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func newStore(t *testing.T) *sqlite.DB {
	t.Helper()
	return openStore(t, dsnFor(t.TempDir(), "cutvideo-test.db"))
}

func testTime() time.Time {
	return time.Date(2026, time.April, 12, 10, 30, 0, 0, time.UTC)
}

func seedUser(t *testing.T, store *sqlite.DB, id string, role domain.Role) *domain.User {
	t.Helper()
	user, err := domain.NewUser(id, id+"@example.com", "Operator "+id, role, "hash", testTime())
	if err != nil {
		t.Fatalf("new user: %v", err)
	}
	if err := store.Users().Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func seedProject(t *testing.T, store *sqlite.DB, id, code, ownerID string) *domain.Project {
	t.Helper()
	project, err := domain.NewProject(id, code, "Project "+code, ownerID, 25, "1920x1080",
		testTime().Add(96*time.Hour), testTime())
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	if err := store.Projects().Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func seedVerifiedAsset(t *testing.T, store *sqlite.DB, id, projectID string, durationMS int64) *domain.MediaAsset {
	t.Helper()
	sum := strings.Repeat(id[len(id)-1:], 64)
	asset, err := domain.NewMediaAsset(id, projectID, id+".mov", "mov", domain.AssetKindVideo, sum,
		1<<22, durationMS, testTime(), 240*time.Hour)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	if err := asset.Verify(sum, testTime()); err != nil {
		t.Fatalf("verify asset: %v", err)
	}
	if err := store.Assets().Create(context.Background(), asset); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return asset
}

func TestMigrationsAreIdempotent(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if version < 4 {
		t.Fatalf("expected at least four applied migrations, got %d", version)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second migrate run must be a no-op: %v", err)
	}
	applied, err := store.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("applied migrations: %v", err)
	}
	if len(applied) != version {
		t.Fatalf("expected %d recorded migrations, got %d", version, len(applied))
	}
}

func TestUserUniquenessAndLookup(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	user := seedUser(t, store, "usr_a", domain.RoleEditor)

	duplicate, err := domain.NewUser("usr_b", user.Email, "Copy", domain.RoleEditor, "hash", testTime())
	if err != nil {
		t.Fatalf("new user: %v", err)
	}
	if err := store.Users().Create(ctx, duplicate); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate email must conflict, got %v", err)
	}
	loaded, err := store.Users().GetByEmail(ctx, strings.ToUpper(user.Email))
	if err != nil {
		t.Fatalf("get by email: %v", err)
	}
	if loaded.ID != user.ID {
		t.Fatalf("expected %s, got %s", user.ID, loaded.ID)
	}
	if _, err := store.Users().GetByID(ctx, "usr_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestProjectListingTotalsMatchFilter(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	owner := seedUser(t, store, "usr_owner", domain.RoleEditor)
	other := seedUser(t, store, "usr_other", domain.RoleEditor)

	for index := 0; index < 7; index++ {
		seedProject(t, store, fmt.Sprintf("prj_%d", index), fmt.Sprintf("REEL%02d", index), owner.ID)
	}
	seedProject(t, store, "prj_other", "OTHER1", other.ID)

	page, err := domain.NewPage(1, 5, "code", domain.SortAscending)
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	result, err := store.Projects().List(ctx, domain.ProjectFilter{OwnerID: owner.ID}, page)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if result.Total != 7 {
		t.Fatalf("filtered total must exclude other owners, got %d", result.Total)
	}
	if len(result.Items) != 5 {
		t.Fatalf("expected 5 items on the first page, got %d", len(result.Items))
	}
	if result.TotalPages != 2 {
		t.Fatalf("expected 2 pages, got %d", result.TotalPages)
	}
	if result.Items[0].Code != "REEL00" {
		t.Fatalf("ascending sort broken, first item is %s", result.Items[0].Code)
	}

	second, err := domain.NewPage(2, 5, "code", domain.SortAscending)
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	tail, err := store.Projects().List(ctx, domain.ProjectFilter{OwnerID: owner.ID}, second)
	if err != nil {
		t.Fatalf("list projects page two: %v", err)
	}
	if len(tail.Items) != 2 {
		t.Fatalf("expected 2 items on the second page, got %d", len(tail.Items))
	}
}

func TestAssetDeclaredChecksumUniquePerProject(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	owner := seedUser(t, store, "usr_c", domain.RoleEditor)
	project := seedProject(t, store, "prj_c", "CHK01", owner.ID)
	asset := seedVerifiedAsset(t, store, "ast_1", project.ID, 30_000)

	same, err := domain.NewMediaAsset("ast_2", project.ID, "again.mov", "mov", domain.AssetKindVideo,
		asset.DeclaredSum, 4096, 1_000, testTime(), time.Hour)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	if err := store.Assets().Create(ctx, same); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate declared checksum must conflict, got %v", err)
	}
	found, err := store.Assets().GetByDeclaredChecksum(ctx, project.ID, strings.ToUpper(asset.DeclaredSum))
	if err != nil {
		t.Fatalf("get by checksum: %v", err)
	}
	if found.ID != asset.ID {
		t.Fatalf("expected %s, got %s", asset.ID, found.ID)
	}
}

func TestTimelineOptimisticUpdateRejectsStaleWriter(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	owner := seedUser(t, store, "usr_d", domain.RoleEditor)
	project := seedProject(t, store, "prj_d", "OPT01", owner.ID)

	version, err := domain.NewTimelineVersion("tml_1", project.ID, 1, owner.ID, "cut", testTime())
	if err != nil {
		t.Fatalf("new version: %v", err)
	}
	if err := store.Timelines().CreateVersion(ctx, version); err != nil {
		t.Fatalf("create version: %v", err)
	}
	first, err := store.Timelines().GetVersion(ctx, version.ID)
	if err != nil {
		t.Fatalf("load version: %v", err)
	}
	second, err := store.Timelines().GetVersion(ctx, version.ID)
	if err != nil {
		t.Fatalf("load version: %v", err)
	}

	first.Notes = "first writer"
	expected := first.RowVersion
	first.RowVersion = expected + 1
	if err := store.Timelines().UpdateVersion(ctx, first, expected); err != nil {
		t.Fatalf("first update: %v", err)
	}
	second.Notes = "second writer"
	second.RowVersion = expected + 1
	if err := store.Timelines().UpdateVersion(ctx, second, expected); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale writer must lose, got %v", err)
	}
	reloaded, err := store.Timelines().GetVersion(ctx, version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	if reloaded.Notes != "first writer" {
		t.Fatalf("stale write must not land, notes are %q", reloaded.Notes)
	}
}

func TestTransactionRollbackLeavesNoPartialState(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	owner := seedUser(t, store, "usr_e", domain.RoleEditor)
	project := seedProject(t, store, "prj_e", "TRX01", owner.ID)

	sentinel := errors.New("audit sink unavailable")
	err := store.InTx(ctx, func(txCtx context.Context) error {
		version, err := domain.NewTimelineVersion("tml_rollback", project.ID, 1, owner.ID, "", testTime())
		if err != nil {
			return err
		}
		if err := store.Timelines().CreateVersion(txCtx, version); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel error, got %v", err)
	}
	if _, err := store.Timelines().GetVersion(ctx, "tml_rollback"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rolled back version must not exist, got %v", err)
	}
}

func TestReserveIdleSlotIsExclusiveUnderConcurrency(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	slot, err := domain.NewRenderSlot("slt_1", "primary-01", "primary", 4, testTime())
	if err != nil {
		t.Fatalf("new slot: %v", err)
	}
	if err := store.Slots().Create(ctx, slot); err != nil {
		t.Fatalf("create slot: %v", err)
	}

	const contenders = 8
	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	wg.Add(contenders)
	for index := 0; index < contenders; index++ {
		jobID := fmt.Sprintf("rnd_%d", index)
		go func() {
			defer wg.Done()
			<-start
			reserved, err := store.Slots().ReserveIdle(ctx, "primary", jobID, testTime())
			if err != nil {
				return
			}
			mu.Lock()
			winners = append(winners, reserved.HeldByJobID)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("exactly one job may hold the single seat, got %d winners", len(winners))
	}
	stored, err := store.Slots().GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("reload slot: %v", err)
	}
	if stored.Status != domain.SlotBusy || stored.HeldByJobID != winners[0] {
		t.Fatalf("stored slot disagrees with the winner: %+v", stored)
	}
	capacity, err := store.Slots().Capacity(ctx, "primary")
	if err != nil {
		t.Fatalf("capacity: %v", err)
	}
	if capacity.Idle != 0 || capacity.Busy != 1 || !capacity.Saturated() {
		t.Fatalf("unexpected capacity %+v", capacity)
	}
}

func TestReserveIdleReportsExhaustedCapacity(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.Slots().ReserveIdle(ctx, "primary", "rnd_1", testTime()); !errors.Is(err, domain.ErrCapacityExhausted) {
		t.Fatalf("expected exhausted capacity, got %v", err)
	}
}

func TestRenderQueueOrderingAndLeaseQueries(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	owner := seedUser(t, store, "usr_f", domain.RoleEditor)
	project := seedProject(t, store, "prj_f", "QUE01", owner.ID)
	version, err := domain.NewTimelineVersion("tml_q", project.ID, 1, owner.ID, "", testTime())
	if err != nil {
		t.Fatalf("new version: %v", err)
	}
	if err := store.Timelines().CreateVersion(ctx, version); err != nil {
		t.Fatalf("create version: %v", err)
	}

	background, _ := domain.NewRenderJob("rnd_bg", project.ID, version.ID, owner.ID, "proxy_540p",
		domain.PriorityBackground, 3, "", testTime())
	urgent, _ := domain.NewRenderJob("rnd_urgent", project.ID, version.ID, owner.ID, "master_2160p",
		domain.PriorityUrgent, 3, "", testTime().Add(time.Minute))
	for _, job := range []*domain.RenderJob{background, urgent} {
		if err := store.Renders().Create(ctx, job); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}
	eligible, err := store.Renders().NextEligible(ctx, testTime().Add(2*time.Minute), 5)
	if err != nil {
		t.Fatalf("next eligible: %v", err)
	}
	if len(eligible) != 2 || eligible[0].ID != urgent.ID {
		t.Fatalf("urgent work must come first, got %+v", eligible)
	}
	active, err := store.Renders().CountActiveForTimeline(ctx, version.ID)
	if err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 2 {
		t.Fatalf("expected 2 active jobs, got %d", active)
	}

	expected := urgent.RowVersion
	if err := urgent.Assign("slt_x", time.Minute, testTime().Add(2*time.Minute)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := store.Renders().Update(ctx, urgent, expected); err != nil {
		t.Fatalf("update: %v", err)
	}
	expiredLeases, err := store.Renders().ListExpiredLeases(ctx, testTime().Add(5*time.Minute), 10)
	if err != nil {
		t.Fatalf("list expired leases: %v", err)
	}
	if len(expiredLeases) != 1 || expiredLeases[0].ID != urgent.ID {
		t.Fatalf("expected the assigned job to show up as an expired lease, got %+v", expiredLeases)
	}
}

func TestRenderIdempotencyKeyIsUniquePerProject(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	owner := seedUser(t, store, "usr_g", domain.RoleEditor)
	project := seedProject(t, store, "prj_g", "IDEM01", owner.ID)
	version, _ := domain.NewTimelineVersion("tml_i", project.ID, 1, owner.ID, "", testTime())
	if err := store.Timelines().CreateVersion(ctx, version); err != nil {
		t.Fatalf("create version: %v", err)
	}
	first, _ := domain.NewRenderJob("rnd_i1", project.ID, version.ID, owner.ID, "web_1080p",
		domain.PriorityNormal, 3, "batch-7", testTime())
	if err := store.Renders().Create(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, _ := domain.NewRenderJob("rnd_i2", project.ID, version.ID, owner.ID, "web_1080p",
		domain.PriorityNormal, 3, "batch-7", testTime())
	if err := store.Renders().Create(ctx, second); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reused idempotency key must conflict, got %v", err)
	}
	found, err := store.Renders().FindByIdempotencyKey(ctx, project.ID, "batch-7")
	if err != nil {
		t.Fatalf("find by key: %v", err)
	}
	if found.ID != first.ID {
		t.Fatalf("expected %s, got %s", first.ID, found.ID)
	}
}

func TestStateSurvivesReopeningTheDatabase(t *testing.T) {
	dir := t.TempDir()
	dsn := dsnFor(dir, "restart.db")
	ctx := context.Background()

	func() {
		store := openStore(t, dsn)
		owner := seedUser(t, store, "usr_h", domain.RoleSupervisor)
		project := seedProject(t, store, "prj_h", "RST01", owner.ID)
		asset := seedVerifiedAsset(t, store, "ast_r", project.ID, 45_000)
		version, _ := domain.NewTimelineVersion("tml_r", project.ID, 1, owner.ID, "restart", testTime())
		if err := store.Timelines().CreateVersion(ctx, version); err != nil {
			t.Fatalf("create version: %v", err)
		}
		clip, err := domain.NewClip("clp_r", version.ID, asset.ID, 0, 0, 10_000, domain.TrackProgram, "", 100, testTime())
		if err != nil {
			t.Fatalf("new clip: %v", err)
		}
		if err := store.Timelines().AddClip(ctx, clip); err != nil {
			t.Fatalf("add clip: %v", err)
		}
	}()

	reopened := openStore(t, dsn)
	version, err := reopened.Timelines().GetVersion(ctx, "tml_r")
	if err != nil {
		t.Fatalf("reload version after restart: %v", err)
	}
	if version.ClipCount != 1 {
		t.Fatalf("clip count must survive a restart, got %d", version.ClipCount)
	}
	if version.TotalDurationMS != 10_000 {
		t.Fatalf("derived duration must survive a restart, got %d", version.TotalDurationMS)
	}
	if _, err := reopened.Assets().GetByID(ctx, "ast_r"); err != nil {
		t.Fatalf("asset must survive a restart: %v", err)
	}
}

func TestDeliveryRecordsAreUniquePerJobAndTarget(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	owner := seedUser(t, store, "usr_i", domain.RoleSupervisor)
	project := seedProject(t, store, "prj_i", "DLV01", owner.ID)
	version, _ := domain.NewTimelineVersion("tml_d", project.ID, 1, owner.ID, "", testTime())
	if err := store.Timelines().CreateVersion(ctx, version); err != nil {
		t.Fatalf("create version: %v", err)
	}
	job, _ := domain.NewRenderJob("rnd_d", project.ID, version.ID, owner.ID, "web_1080p", domain.PriorityNormal, 3, "", testTime())
	if err := store.Renders().Create(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	target, err := domain.NewDeliveryTarget("dst_1", project.ID, "broadcast", domain.DeliveryWebhook,
		"https://example.invalid/hook", "vault://cred", testTime())
	if err != nil {
		t.Fatalf("new target: %v", err)
	}
	if err := store.Deliveries().CreateTarget(ctx, target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	record, err := domain.NewDeliveryRecord("dlv_1", job.ID, target.ID, project.ID, "cutvideo://out.mov", 3, testTime())
	if err != nil {
		t.Fatalf("new record: %v", err)
	}
	if err := store.Deliveries().CreateRecord(ctx, record); err != nil {
		t.Fatalf("create record: %v", err)
	}
	duplicate, _ := domain.NewDeliveryRecord("dlv_2", job.ID, target.ID, project.ID, "cutvideo://out.mov", 3, testTime())
	if err := store.Deliveries().CreateRecord(ctx, duplicate); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate delivery record must conflict, got %v", err)
	}
	records, err := store.Deliveries().ListRecordsForJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly one record, got %d", len(records))
	}
}

func TestAuditFilteringAndIdempotencyExpiry(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for index := 0; index < 3; index++ {
		event, err := domain.NewAuditEvent(fmt.Sprintf("aud_%d", index), "usr_1", domain.RoleEditor,
			domain.ActionRenderSubmit, "render_job", fmt.Sprintf("rnd_%d", index), domain.AuditSucceeded,
			"req_1", "", testTime().Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatalf("new event: %v", err)
		}
		if err := store.Audits().Append(ctx, event); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	rejected, err := domain.NewAuditEvent("aud_x", "usr_1", domain.RoleEditor, domain.ActionRenderCancel,
		"render_job", "rnd_0", domain.AuditRejected, "req_2", "no authority", testTime())
	if err != nil {
		t.Fatalf("new event: %v", err)
	}
	if err := store.Audits().Append(ctx, rejected); err != nil {
		t.Fatalf("append event: %v", err)
	}

	page, _ := domain.NewPage(1, 10, "created_at", domain.SortDescending)
	result, err := store.Audits().List(ctx, domain.AuditFilter{Action: domain.ActionRenderSubmit}, page)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("expected 3 submit events, got %d", result.Total)
	}

	record, err := domain.NewIdempotencyRecord("idem_1", "render.submit", "POST", "/api/v1/renders",
		"usr_1", "key-1", "hash", 202, "{}", testTime(), time.Hour)
	if err != nil {
		t.Fatalf("new idempotency record: %v", err)
	}
	if err := store.Idempotency().Put(ctx, record); err != nil {
		t.Fatalf("put record: %v", err)
	}
	if _, err := store.Idempotency().Get(ctx, "render.submit", "post", "/api/v1/renders", "usr_1", "key-1"); err != nil {
		t.Fatalf("get record: %v", err)
	}
	dropped, err := store.Idempotency().DeleteExpired(ctx, testTime().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("expected one expired record, got %d", dropped)
	}
}

func TestSessionRevocationAndPurge(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	user := seedUser(t, store, "usr_j", domain.RoleEditor)

	live, err := domain.NewSession("ses_live", user.ID, "hash-live", "agent", testTime(), 2*time.Hour)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	stale, err := domain.NewSession("ses_stale", user.ID, "hash-stale", "agent", testTime().Add(-4*time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	for _, session := range []*domain.Session{live, stale} {
		if err := store.Sessions().Create(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	purged, err := store.Sessions().DeleteExpired(ctx, testTime())
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if purged != 1 {
		t.Fatalf("expected one purged session, got %d", purged)
	}
	revoked, err := store.Sessions().RevokeAllForUser(ctx, user.ID, testTime())
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if revoked != 1 {
		t.Fatalf("expected one revoked session, got %d", revoked)
	}
	reloaded, err := store.Sessions().GetByTokenHash(ctx, "hash-live")
	if err != nil {
		t.Fatalf("get by token hash: %v", err)
	}
	if !reloaded.Revoked() {
		t.Fatal("session must be marked revoked")
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	orphan, err := domain.NewProject("prj_orphan", "ORP01", "Orphan", "usr_missing", 24, "1920x1080",
		testTime().Add(time.Hour), testTime())
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	if err := store.Projects().Create(ctx, orphan); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("missing owner must be rejected by the foreign key, got %v", err)
	}
}
