// Package config reads settings from the environment. Secrets are never logged.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fiat/content-studio/internal/notify"
)

type Config struct {
	DatabaseURL string
	Addr        string
	StorageRoot string
	ExportDir   string
	WebDir      string

	// Passkey authentication (D9). AuthRPID is the WebAuthn relying-party ID (a domain, never an IP);
	// AuthOrigins are the browser origins allowed to call the API and to run WebAuthn ceremonies.
	AuthRPID      string
	AuthOrigins   []string
	SessionIdle   time.Duration
	SessionMaxAge time.Duration

	OllamaURL string
	LLMModel  string
	LLMNumCtx int

	ComfyURL        string
	ImageCheckpoint string
	ImageSteps      int
	ImageCFG        float64
	LoraStrength    float64
	ImageSampler    string
	ImageScheduler  string
	ImageWidth      int
	ImageHeight     int

	ThaiVoiceURL string

	// CloudEnabled is backend configuration only (default off). Even when on, no cloud adapter or
	// budget check exists yet, so every cloud request is still refused.
	CloudEnabled bool

	// DiscordWebhookURL is a secret (notifications.md). Env only; never log or return it.
	DiscordWebhookURL string

	FFmpeg       string
	FFprobe      string
	FPS          int
	SubtitleFont string

	MaxAttempts   int
	LeaseSeconds  int
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
	c.DiscordWebhookURL = os.Getenv("STUDIO_DISCORD_WEBHOOK_URL")
	if c.DiscordWebhookURL != "" {
		if err := notify.ValidateWebhookURL(c.DiscordWebhookURL); err != nil {
			return c, fmt.Errorf("STUDIO_DISCORD_WEBHOOK_URL: %w", err) // the value is deliberately not echoed
		}
	}
	var err error
	switch v := os.Getenv("STUDIO_CLOUD_ENABLED"); v {
	case "", "false":
	case "true":
		c.CloudEnabled = true
	default:
		return c, fmt.Errorf("STUDIO_CLOUD_ENABLED must be true or false")
	}
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
		{"STUDIO_LEASE_SECONDS", 60, &c.LeaseSeconds},
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
	if c.LoraStrength, err = strconv.ParseFloat(env("STUDIO_LORA_STRENGTH", "0.8"), 64); err != nil || c.LoraStrength <= 0 || c.LoraStrength > 2 {
		return c, fmt.Errorf("STUDIO_LORA_STRENGTH must be a number above 0 and at most 2")
	}
	if err := c.loadAuth(); err != nil {
		return c, err
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

// loadAuth reads STUDIO_AUTH_RP_ID, STUDIO_AUTH_ORIGINS (comma separated), STUDIO_SESSION_IDLE and
// STUDIO_SESSION_MAX. Defaults serve `make dev`: the app on STUDIO_ADDR's port and the Vite dev server
// on 5173, both reached as localhost (a WebAuthn RP ID cannot be an IP, so open http://localhost:PORT).
// The 127.0.0.1 forms are allowed as origins for the CSRF check only; browsers will not run a
// passkey ceremony for RP ID "localhost" from a 127.0.0.1 page.
func (c *Config) loadAuth() error {
	c.AuthRPID = env("STUDIO_AUTH_RP_ID", "localhost")
	_, port, _ := net.SplitHostPort(c.Addr)
	c.AuthOrigins = []string{"http://localhost:" + port, "http://127.0.0.1:" + port, "http://localhost:5173", "http://127.0.0.1:5173"}
	if v := os.Getenv("STUDIO_AUTH_ORIGINS"); v != "" {
		c.AuthOrigins = nil
		for _, o := range strings.Split(v, ",") {
			c.AuthOrigins = append(c.AuthOrigins, strings.TrimSpace(o))
		}
	}
	for _, o := range c.AuthOrigins {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return fmt.Errorf("STUDIO_AUTH_ORIGINS: %q is not an origin like https://host[:port]", o)
		}
		if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("STUDIO_AUTH_ORIGINS: %q: plain http is only allowed for localhost; use https", o)
		}
	}
	var err error
	if c.SessionIdle, err = envDuration("STUDIO_SESSION_IDLE", 12*time.Hour); err != nil {
		return err
	}
	if c.SessionMaxAge, err = envDuration("STUDIO_SESSION_MAX", 7*24*time.Hour); err != nil {
		return err
	}
	return nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("%s must be a duration of at least 1m, like 12h", key)
	}
	return d, nil
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
