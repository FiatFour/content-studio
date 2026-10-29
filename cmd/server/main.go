// Command server is the studio HTTP API. It binds to localhost only.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/config"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/notify"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("svc", "server")
	if err := run(log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	st := &artifacts.Store{Root: cfg.StorageRoot}
	if err := os.MkdirAll(cfg.StorageRoot, 0o755); err != nil {
		return err
	}
	srv := &api.Server{
		DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: cfg.MaxAttempts}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: cfg.ExportDir}, WebDir: cfg.WebDir, Log: log,
	}
	srv.Notify = &notify.Service{DB: db, Log: log}
	if cfg.DiscordWebhookURL != "" {
		d, err := notify.NewDiscord(cfg.DiscordWebhookURL, nil)
		if err != nil {
			return err
		}
		srv.Notify.Notifier, srv.Notify.Suffix = d, d.Suffix()
	}
	if _, err := os.Stat(cfg.WebDir); err != nil {
		srv.WebDir = ""
	}
	hs := &http.Server{Addr: cfg.Addr, Handler: srv.Echo(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hs.Shutdown(shut); err != nil {
			log.Error("shutdown", "err", err)
		}
	}()
	log.Info("listening", "addr", cfg.Addr, "web", srv.WebDir != "")
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
