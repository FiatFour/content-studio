// Command server is the studio HTTP API. It binds to localhost only.
//
//	server           run the API
//	server enroll    print a one-time code (valid 10 min) that registers the FIRST passkey, to this terminal only
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiat/content-studio/internal/api"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth"
	"github.com/fiat/content-studio/internal/config"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/notify"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("svc", "server")
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "enroll":
		err = enroll(os.Stdout)
	case len(os.Args) == 1:
		err = run(log)
	default:
		fmt.Fprintln(os.Stderr, "usage: server [enroll]")
		os.Exit(2)
	}
	if err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func newAuth(cfg config.Config, db *pgxpool.Pool) (*auth.Service, error) {
	return auth.New(db, auth.Config{RPID: cfg.AuthRPID, Origins: cfg.AuthOrigins, IdleTTL: cfg.SessionIdle, MaxTTL: cfg.SessionMaxAge})
}

// enroll prints the code to out (the terminal) and nowhere else: not the logger, not the database in
// plaintext, not the audit log.
func enroll(out io.Writer) error {
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
	svc, err := newAuth(cfg, db)
	if err != nil {
		return err
	}
	code, exp, err := svc.NewEnrollCode(ctx)
	if errors.Is(err, auth.ErrAlreadyEnrolled) {
		return errors.New("a passkey is already enrolled; sign in and add another from Settings (recovery: see docs/spec/security-ops.md)")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Enrollment code: %s\nValid until %s (10 minutes), single use.\nOpen %s, choose enrollment, and enter the code.\n",
		code, exp.Local().Format("15:04:05"), cfg.AuthOrigins[0])
	return nil
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
		CloudEnabled: cfg.CloudEnabled, ExportDir: cfg.ExportDir,
		Health: []api.HealthTarget{
			{Name: "ollama", Capability: providers.CapLLM, URL: cfg.OllamaURL, Path: "/api/tags"},
			{Name: "comfyui", Capability: providers.CapImage, URL: cfg.ComfyURL, Path: "/system_stats"},
			{Name: "thaivoice", Capability: providers.CapTTS, URL: cfg.ThaiVoiceURL, Path: "/health"},
		},
	}
	if srv.Auth, err = newAuth(cfg, db); err != nil {
		return err
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
	srv.Limits = api.Limits{MaxBody: cfg.MaxBodyBytes, AnonPerMinute: cfg.RateAnonPerM, AuthPerMinute: cfg.RateAuthPerM, StreamWrite: cfg.StreamTimeout}
	hs := &http.Server{
		Addr: cfg.Addr, Handler: srv.Echo(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout,
		MaxHeaderBytes: 32 << 10, // artifact downloads extend their own write deadline (api.Limits.StreamWrite)
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hs.Shutdown(shut); err != nil {
			log.Error("shutdown", "err", err)
		}
	}()
	log.Info("listening", "addr", cfg.Addr, "web", srv.WebDir != "", "tls", cfg.TLSCert != "")
	if cfg.AllowInsecureRemote {
		log.Warn("STUDIO_ALLOW_INSECURE_REMOTE is set: plain http may be reachable off this machine")
	}
	if cfg.TLSCert != "" {
		hs.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		err = hs.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		err = hs.ListenAndServe()
	}
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
