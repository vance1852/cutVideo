// Package app wires configuration, storage, services, HTTP and background
// workers into one runnable application.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/vance1852/cutVideo/internal/audit"
	"github.com/vance1852/cutVideo/internal/clock"
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/httpapi"
	"github.com/vance1852/cutVideo/internal/idempotency"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
	"github.com/vance1852/cutVideo/internal/service/auth"
	"github.com/vance1852/cutVideo/internal/service/delivery"
	"github.com/vance1852/cutVideo/internal/service/editing"
	"github.com/vance1852/cutVideo/internal/service/media"
	"github.com/vance1852/cutVideo/internal/service/render"
	"github.com/vance1852/cutVideo/internal/storage/sqlite"
	"github.com/vance1852/cutVideo/internal/worker"
)

// Version is stamped into health responses.
const Version = "1.0.0"

// App owns every long lived component of the service.
type App struct {
	Config   config.Config
	Logger   *slog.Logger
	Store    *sqlite.DB
	Auth     *auth.Service
	Media    *media.Service
	Editing  *editing.Service
	Render   *render.Service
	Delivery *delivery.Service
	Recorder *audit.Recorder
	Guard    *idempotency.Guard
	Workers  *worker.Pool
	Reaper   *worker.Reaper
	Handler  http.Handler

	clk clock.Clock
	gen ids.Generator
}

// Options allows tests to inject deterministic collaborators.
type Options struct {
	Clock     clock.Clock
	IDs       ids.Generator
	Renderer  worker.Renderer
	Transport delivery.Transport
	Logger    *slog.Logger
}

// New builds the application from configuration.
func New(ctx context.Context, cfg config.Config, opts Options) (*App, error) {
	logger := opts.Logger
	if logger == nil {
		logger = logging.New(cfg.Logging.Level, cfg.Logging.Format, nil)
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.SystemClock{}
	}
	gen := opts.IDs
	if gen == nil {
		gen = ids.RandomGenerator{}
	}
	store, err := sqlite.Open(ctx, cfg.Database, logger)
	if err != nil {
		return nil, err
	}
	recorder := audit.NewRecorder(store.Audits(), gen, clk, logger)
	guard := idempotency.NewGuard(store.Idempotency(), gen, clk, cfg.Auth.IdempotencyTTL)

	transport := opts.Transport
	if transport == nil {
		transport = delivery.NewHTTPTransport(&http.Client{Timeout: 10 * time.Second}, 10*time.Second)
	}

	authService := auth.New(store, recorder, gen, clk, cfg.Auth, logger)
	mediaService := media.New(store, recorder, gen, clk, cfg, logger)
	editingService := editing.New(store, recorder, gen, clk, logger)
	renderService := render.New(store, recorder, guard, gen, clk, cfg, logger)
	deliveryService := delivery.New(store, recorder, transport, gen, clk, logger)

	renderer := opts.Renderer
	if renderer == nil {
		renderer = worker.SyntheticRenderer{}
	}
	pool := worker.NewPool(renderService, renderer, cfg.Worker, render.DefaultPool, logger)
	reaper := worker.NewReaper(renderService, authService, guard, cfg.Worker.ReaperPeriod, logger)

	router := httpapi.NewRouter(httpapi.Dependencies{
		Store:    store,
		Auth:     authService,
		Media:    mediaService,
		Editing:  editingService,
		Render:   renderService,
		Delivery: deliveryService,
		Recorder: recorder,
		Clock:    clk,
		IDs:      gen,
		Config:   cfg,
		Logger:   logger,
		Version:  Version,
	})

	return &App{
		Config:   cfg,
		Logger:   logger,
		Store:    store,
		Auth:     authService,
		Media:    mediaService,
		Editing:  editingService,
		Render:   renderService,
		Delivery: deliveryService,
		Recorder: recorder,
		Guard:    guard,
		Workers:  pool,
		Reaper:   reaper,
		Handler:  router.Handler(),
		clk:      clk,
		gen:      gen,
	}, nil
}

// Close releases the store.
func (a *App) Close() error { return a.Store.Close() }

// Run serves HTTP until the context is canceled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	if a.Config.Worker.Enabled {
		a.Workers.Start(workerCtx)
		a.Reaper.Start(workerCtx)
	}

	server := &http.Server{
		Addr:              a.Config.HTTP.Addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		a.Logger.Info("cutVideo listening", "addr", a.Config.HTTP.Addr, "env", a.Config.Env)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("app: http server: %w", err)
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		stopWorkers()
		a.Workers.Stop()
		a.Reaper.Stop()
		return err
	case <-ctx.Done():
		a.Logger.Info("shutdown requested", "grace", a.Config.HTTP.ShutdownGrace.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.Config.HTTP.ShutdownGrace)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	stopWorkers()
	a.Workers.Stop()
	a.Reaper.Stop()
	if shutdownErr != nil {
		return fmt.Errorf("app: graceful shutdown: %w", shutdownErr)
	}
	return nil
}

// Bootstrap creates the first supervisor account and render seats when the
// database is empty. It is idempotent: an existing installation is left alone.
func (a *App) Bootstrap(ctx context.Context, email, password string) error {
	page, err := a.Store.Users().List(ctx, domainPage())
	if err != nil {
		return fmt.Errorf("app: inspect users: %w", err)
	}
	if page.Total == 0 {
		if _, err := a.Auth.Provision(ctx, principalZero(), auth.ProvisionInput{
			Email:       email,
			DisplayName: "Bootstrap Supervisor",
			Role:        roleSupervisor(),
			Password:    password,
		}); err != nil {
			return fmt.Errorf("app: provision bootstrap supervisor: %w", err)
		}
		a.Logger.Info("bootstrap supervisor created", "email", email)
	}
	slots, err := a.Store.Slots().List(ctx, render.DefaultPool)
	if err != nil {
		return fmt.Errorf("app: inspect render seats: %w", err)
	}
	if len(slots) == 0 {
		supervisor := principalSupervisor()
		for index := 1; index <= 2; index++ {
			if _, err := a.Render.ProvisionSlot(ctx, supervisor,
				fmt.Sprintf("%s-%02d", render.DefaultPool, index), render.DefaultPool, 4); err != nil {
				return fmt.Errorf("app: provision render seat: %w", err)
			}
		}
		a.Logger.Info("render seats provisioned", "pool", render.DefaultPool, "count", 2)
	}
	return nil
}
