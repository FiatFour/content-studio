// Package config reads settings from the environment. Secrets are never logged.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL string
	Addr        string
	StorageRoot string
	ExportDir   string
	WebDir      string

	OllamaURL string
	LLMModel  string
	LLMNumCtx int

	ComfyURL        string
	ImageCheckpoint string
	ImageSteps      int
	ImageCFG        float64
	ImageSampler    string
	ImageScheduler  string
	ImageWidth      int
	ImageHeight     int

	ThaiVoiceURL string

	FFmpeg       string
	FFprobe      string
	FPS          int
	SubtitleFont string

	MaxAttempts   int
	ScriptRetries int
	PollInterval  time.Duration
	StageTimeout  time.Duration
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL: env("STUDIO_DATABASE_URL", "postgres://studio:studio@127.0.0.1:5433/studio"),
		Addr:        env("STUDIO_ADDR", "127.0.0.1:8080"),
		StorageRoot: env("STUDIO_STORAGE_ROOT", "var/storage"),
		ExportDir:   env("STUDIO_EXPORT_DIR", "var/exports"),
		WebDir:      env("STUDIO_WEB_DIR", "web/dist"),

		OllamaURL: env("OLLAMA_URL", "http://127.0.0.1:11434"),
		LLMModel:  env("STUDIO_LLM_MODEL", "qwen3:8b"),

		ComfyURL:        env("COMFYUI_URL", "http://127.0.0.1:8188"),
		ImageCheckpoint: env("STUDIO_IMAGE_CHECKPOINT", "DreamShaperXL_Turbo_v2_1.safetensors"),
		ImageSampler:    env("STUDIO_IMAGE_SAMPLER", "dpmpp_sde"),
		ImageScheduler:  env("STUDIO_IMAGE_SCHEDULER", "karras"),

		ThaiVoiceURL: env("THAIVOICE_URL", "http://127.0.0.1:8190"),

		FFmpeg:       env("FFMPEG_BIN", "/usr/bin/ffmpeg"),
		FFprobe:      env("FFPROBE_BIN", "/usr/bin/ffprobe"),
		SubtitleFont: env("STUDIO_SUBTITLE_FONT", "Noto Sans Thai"),
	}
	var err error
	ints := []struct {
		key string
		def int
		dst *int
	}{
		{"STUDIO_LLM_NUM_CTX", 8192, &c.LLMNumCtx},
		{"STUDIO_IMAGE_STEPS", 8, &c.ImageSteps},
		{"STUDIO_IMAGE_WIDTH", 768, &c.ImageWidth},
		{"STUDIO_IMAGE_HEIGHT", 1344, &c.ImageHeight},
		{"STUDIO_FPS", 30, &c.FPS},
		{"STUDIO_MAX_ATTEMPTS", 3, &c.MaxAttempts},
		{"STUDIO_SCRIPT_RETRIES", 3, &c.ScriptRetries},
	}
	for _, i := range ints {
		if *i.dst, err = envInt(i.key, i.def); err != nil {
			return c, err
		}
	}
	if c.ImageCFG, err = strconv.ParseFloat(env("STUDIO_IMAGE_CFG", "2.0"), 64); err != nil {
		return c, fmt.Errorf("STUDIO_IMAGE_CFG: %w", err)
	}
	c.PollInterval = time.Second
	c.StageTimeout = 45 * time.Minute
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c, fmt.Errorf("STUDIO_ADDR: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return c, fmt.Errorf("STUDIO_ADDR must be a loopback address in M1a, got %q", host)
	}
	for name, raw := range map[string]string{"OLLAMA_URL": c.OllamaURL, "COMFYUI_URL": c.ComfyURL, "THAIVOICE_URL": c.ThaiVoiceURL} {
		if err := requireLocal(name, raw); err != nil {
			return c, err
		}
	}
	return c, nil
}

// requireLocal enforces non-negotiable 1 for M1a: every AI provider runs on this machine.
// There are no cloud adapters, and a provider URL pointing off-machine is refused at startup.
func requireLocal(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s is not a valid URL", name)
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%s must point to this machine (loopback) in M1a; cloud providers are not allowed", name)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
