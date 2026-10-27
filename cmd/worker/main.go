// Command worker runs pipeline jobs one at a time against the local AI services.
package main

import (
	"context"
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
	q := &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: cfg.MaxAttempts}
	deps := &pipeline.Deps{
		DB: db, Store: &artifacts.Store{Root: cfg.StorageRoot}, Queue: q,
		LLM: llm, Images: comfy, Voice: voice, Segmenter: voice,
		GPU:   &providers.GPU{Holders: []providers.GPUHolder{llm, comfy, voice}},
		Tools: media.Tools{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe},
		Settings: pipeline.Settings{ImageWidth: cfg.ImageWidth, ImageHeight: cfg.ImageHeight, OutWidth: 1080, OutHeight: 1920,
			FPS: cfg.FPS, SubtitleFont: cfg.SubtitleFont, ScriptRetries: cfg.ScriptRetries},
		Log: log,
	}
	w := &jobs.Worker{Queue: q, Stages: deps.Stages(), Log: log, PollInterval: cfg.PollInterval, RetryDelay: 10 * time.Second}
	log.Info("worker started", "llm", cfg.LLMModel, "checkpoint", cfg.ImageCheckpoint)
	return w.Run(ctx)
}
