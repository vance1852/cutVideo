package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/vance1852/cutVideo/internal/audit"
	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/middleware"
	"github.com/vance1852/cutVideo/internal/repository"
	"github.com/vance1852/cutVideo/internal/service/auth"
	"github.com/vance1852/cutVideo/internal/service/delivery"
	"github.com/vance1852/cutVideo/internal/service/editing"
	"github.com/vance1852/cutVideo/internal/service/media"
	"github.com/vance1852/cutVideo/internal/service/render"
)

// Dependencies carries everything the HTTP layer needs.
type Dependencies struct {
	Store    repository.Store
	Auth     *auth.Service
	Media    *media.Service
	Editing  *editing.Service
	Render   *render.Service
	Delivery *delivery.Service
	Recorder *audit.Recorder
	Clock    clock.Clock
	IDs      ids.Generator
	Config   config.Config
	Logger   *slog.Logger
	Version  string
}

// Router wires HTTP routes onto the service layer.
type Router struct {
	store    repository.Store
	auth     *auth.Service
	media    *media.Service
	editing  *editing.Service
	render   *render.Service
	delivery *delivery.Service
	recorder *audit.Recorder
	clk      clock.Clock
	gen      ids.Generator
	cfg      config.Config
	logger   *slog.Logger
	version  string
}

// NewRouter builds the router.
func NewRouter(deps Dependencies) *Router {
	logger := deps.Logger
	if logger == nil {
		logger = logging.Discard()
	}
	version := deps.Version
	if version == "" {
		version = "dev"
	}
	return &Router{
		store:    deps.Store,
		auth:     deps.Auth,
		media:    deps.Media,
		editing:  deps.Editing,
		render:   deps.Render,
		delivery: deps.Delivery,
		recorder: deps.Recorder,
		clk:      deps.Clock,
		gen:      deps.IDs,
		cfg:      deps.Config,
		logger:   logger,
		version:  version,
	}
}

// Handler returns the fully decorated HTTP handler.
func (rt *Router) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public endpoints.
	mux.HandleFunc("GET /healthz", rt.handleLiveness)
	mux.HandleFunc("GET /readyz", rt.handleReadiness)
	mux.HandleFunc("POST /api/v1/sessions", rt.handleSignIn)
	mux.HandleFunc("/", rt.handleNotFound)

	authenticated := http.NewServeMux()
	authenticated.HandleFunc("DELETE /api/v1/sessions/current", rt.handleSignOut)
	authenticated.HandleFunc("GET /api/v1/sessions/current", rt.handleWhoAmI)
	authenticated.HandleFunc("GET /api/v1/users", rt.handleListUsers)
	authenticated.HandleFunc("GET /api/v1/audit-events", rt.handleListAudit)

	authenticated.HandleFunc("POST /api/v1/projects", rt.handleCreateProject)
	authenticated.HandleFunc("GET /api/v1/projects", rt.handleListProjects)
	authenticated.HandleFunc("GET /api/v1/projects/{projectID}", rt.handleGetProject)
	authenticated.HandleFunc("POST /api/v1/projects/{projectID}/lock", rt.handleLockProject)
	authenticated.HandleFunc("POST /api/v1/projects/{projectID}/unlock", rt.handleUnlockProject)

	authenticated.HandleFunc("POST /api/v1/projects/{projectID}/assets", rt.handleIngestAsset)
	authenticated.HandleFunc("GET /api/v1/projects/{projectID}/assets", rt.handleListAssets)
	authenticated.HandleFunc("GET /api/v1/assets/{assetID}", rt.handleGetAsset)
	authenticated.HandleFunc("POST /api/v1/assets/{assetID}/verify", rt.handleVerifyAsset)
	authenticated.HandleFunc("POST /api/v1/assets/{assetID}/quarantine", rt.handleQuarantineAsset)
	authenticated.HandleFunc("POST /api/v1/assets/{assetID}/retention", rt.handleExtendRetention)
	authenticated.HandleFunc("POST /api/v1/assets/verify-batch", rt.handleVerifyBatch)

	authenticated.HandleFunc("POST /api/v1/projects/{projectID}/timelines", rt.handleCreateDraft)
	authenticated.HandleFunc("GET /api/v1/projects/{projectID}/timelines", rt.handleListTimelines)
	authenticated.HandleFunc("GET /api/v1/timelines/{timelineID}", rt.handleGetTimeline)
	authenticated.HandleFunc("POST /api/v1/timelines/{timelineID}/clips", rt.handleAddClip)
	authenticated.HandleFunc("DELETE /api/v1/timelines/{timelineID}/clips/{clipID}", rt.handleRemoveClip)
	authenticated.HandleFunc("POST /api/v1/timelines/{timelineID}/seal", rt.handleSealTimeline)

	authenticated.HandleFunc("POST /api/v1/renders", rt.handleSubmitRender)
	authenticated.HandleFunc("GET /api/v1/renders", rt.handleListRenders)
	authenticated.HandleFunc("GET /api/v1/renders/{jobID}", rt.handleGetRender)
	authenticated.HandleFunc("POST /api/v1/renders/{jobID}/cancel", rt.handleCancelRender)
	authenticated.HandleFunc("GET /api/v1/render-farm/capacity", rt.handleFarmCapacity)
	authenticated.HandleFunc("GET /api/v1/renders/{jobID}/deliveries", rt.handleListDeliveryRecords)
	authenticated.HandleFunc("POST /api/v1/renders/{jobID}/deliveries/dispatch", rt.handleDispatchDeliveries)

	authenticated.HandleFunc("GET /api/v1/projects/{projectID}/delivery-targets", rt.handleListTargets)

	// Supervisor only surface.
	supervisor := http.NewServeMux()
	supervisor.HandleFunc("POST /api/v1/users", rt.handleProvisionUser)
	supervisor.HandleFunc("DELETE /api/v1/users/{userID}/sessions", rt.handleRevokeSessions)
	supervisor.HandleFunc("POST /api/v1/render-farm/slots", rt.handleProvisionSlot)
	// Maintaining where a cut is delivered — adding, enabling or disabling a
	// destination — is a supervisor responsibility. Editors may only read the
	// destination list and dispatch their own finished renders.
	supervisor.HandleFunc("POST /api/v1/projects/{projectID}/delivery-targets", rt.handleCreateTarget)
	supervisor.HandleFunc("PATCH /api/v1/delivery-targets/{targetID}", rt.handleSetTargetState)

	authenticatedHandler := middleware.Chain(authenticated, middleware.Authenticate(rt.auth, rt.logger))
	supervisorHandler := middleware.Chain(supervisor,
		middleware.Authenticate(rt.auth, rt.logger),
		middleware.RequireRole(domain.RoleSupervisor),
	)

	root := http.NewServeMux()
	root.Handle("/", mux)
	for _, pattern := range authenticatedPatterns {
		root.Handle(pattern, authenticatedHandler)
	}
	for _, pattern := range supervisorPatterns {
		root.Handle(pattern, supervisorHandler)
	}

	return middleware.Chain(root,
		middleware.RequestID(rt.gen),
		middleware.AccessLog(rt.logger),
		middleware.Recover(rt.logger),
		middleware.Timeout(rt.cfg.HTTP.RequestTimeout),
	)
}

// authenticatedPatterns routes that require a valid session.
var authenticatedPatterns = []string{
	"DELETE /api/v1/sessions/current",
	"GET /api/v1/sessions/current",
	"GET /api/v1/users",
	"GET /api/v1/audit-events",
	"POST /api/v1/projects",
	"GET /api/v1/projects",
	"GET /api/v1/projects/{projectID}",
	"POST /api/v1/projects/{projectID}/lock",
	"POST /api/v1/projects/{projectID}/unlock",
	"POST /api/v1/projects/{projectID}/assets",
	"GET /api/v1/projects/{projectID}/assets",
	"GET /api/v1/assets/{assetID}",
	"POST /api/v1/assets/{assetID}/verify",
	"POST /api/v1/assets/{assetID}/quarantine",
	"POST /api/v1/assets/{assetID}/retention",
	"POST /api/v1/assets/verify-batch",
	"POST /api/v1/projects/{projectID}/timelines",
	"GET /api/v1/projects/{projectID}/timelines",
	"GET /api/v1/timelines/{timelineID}",
	"POST /api/v1/timelines/{timelineID}/clips",
	"DELETE /api/v1/timelines/{timelineID}/clips/{clipID}",
	"POST /api/v1/timelines/{timelineID}/seal",
	"POST /api/v1/renders",
	"GET /api/v1/renders",
	"GET /api/v1/renders/{jobID}",
	"POST /api/v1/renders/{jobID}/cancel",
	"GET /api/v1/render-farm/capacity",
	"GET /api/v1/renders/{jobID}/deliveries",
	"POST /api/v1/renders/{jobID}/deliveries/dispatch",
	"GET /api/v1/projects/{projectID}/delivery-targets",
}

// supervisorPatterns routes that additionally require the supervisor role.
var supervisorPatterns = []string{
	"POST /api/v1/users",
	"DELETE /api/v1/users/{userID}/sessions",
	"POST /api/v1/render-farm/slots",
	"POST /api/v1/projects/{projectID}/delivery-targets",
	"PATCH /api/v1/delivery-targets/{targetID}",
}
