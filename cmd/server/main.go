// Command server runs the cutVideo render orchestration API and its background
// render workers.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/vance1852/cutVideo/internal/app"
	"github.com/vance1852/cutVideo/internal/config"
	"github.com/vance1852/cutVideo/internal/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cutVideo: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(cfg.Logging.Level, cfg.Logging.Format, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, app.Options{Logger: logger})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			logger.Error("closing the store failed", "error", closeErr.Error())
		}
	}()

	email := strings.TrimSpace(os.Getenv("CUTVIDEO_BOOTSTRAP_EMAIL"))
	password := os.Getenv("CUTVIDEO_BOOTSTRAP_PASSWORD")
	if email != "" && password != "" {
		if err := application.Bootstrap(ctx, email, password); err != nil {
			return err
		}
	}

	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("cutVideo stopped")
	return nil
}
