// Command worker runs pipeline jobs one at a time against the local AI services.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/config"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/notify"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("svc", "worker")
	if err := run(log); err != nil {
		log.Error("worker stopped", "err", err)
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
	// Model calls can take minutes; the stage context still cancels them.
	client := &http.Client{Timeout: 15 * time.Minute}
	llm := &providers.Ollama{BaseURL: cfg.OllamaURL, Model: cfg.LLMModel, NumCtx: cfg.LLMNumCtx, Client: client}
	comfy := &providers.ComfyUI{BaseURL: cfg.ComfyURL, Checkpoint: cfg.ImageCheckpoint, Steps: cfg.ImageSteps, CFG: cfg.ImageCFG,
		Sampler: cfg.ImageSampler, Scheduler: cfg.ImageScheduler, Client: client}
	voice := &providers.ThaiVoice{BaseURL: cfg.ThaiVoiceURL, Client: client}
	if cfg.LeaseSeconds < 3 {
		return fmt.Errorf("STUDIO_LEASE_SECONDS must be at least 3")
	}
	q := &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: cfg.MaxAttempts,
		Owner: workerID(), LeaseTTL: time.Duration(cfg.LeaseSeconds) * time.Second}
	deps := &pipeline.Deps{
		DB: db, Store: &artifacts.Store{Root: cfg.StorageRoot}, Queue: q,
		LLM: llm, Images: comfy, Voice: voice, Segmenter: voice,
		GPU:   &providers.GPU{Holders: []providers.GPUHolder{llm, comfy, voice}},
		Tools: media.Tools{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe},
		Settings: pipeline.Settings{ImageWidth: cfg.ImageWidth, ImageHeight: cfg.ImageHeight, OutWidth: 1080, OutHeight: 1920,
			FPS: cfg.FPS, SubtitleFont: cfg.SubtitleFont, ScriptRetries: cfg.ScriptRetries, LoraStrength: cfg.LoraStrength},
		Log: log,
	}
	// Notifications are delivered by their own loop with their own retries; a Discord outage
	// never touches the job worker below.
	ns := &notify.Service{DB: db, Log: log}
	if cfg.DiscordWebhookURL != "" {
		d, err := notify.NewDiscord(cfg.DiscordWebhookURL, nil)
		if err != nil {
			return err
		}
		ns.Notifier, ns.Suffix = d, d.Suffix()
	}
	go ns.Run(ctx)
	w := &jobs.Worker{Queue: q, Stages: deps.Stages(), Log: log, PollInterval: cfg.PollInterval}
	log.Info("worker started", "llm", cfg.LLMModel, "checkpoint", cfg.ImageCheckpoint)
	return w.Run(ctx)
}

// workerID names this process on its job leases. The random suffix keeps a restarted worker from
// being mistaken for its dead predecessor.
func workerID() string {
	host, err := os.Hostname()
	if err != nil {
		host = "host"
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b))
}
