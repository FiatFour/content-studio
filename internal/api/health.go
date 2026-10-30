package api

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/providers"
)

// HealthTarget is one local service the health page may probe. Probes are read-only GETs of a fixed
// path on the configured URL; nothing a client sends chooses the host, path or method.
type HealthTarget struct {
	Name       string
	Capability providers.Capability
	URL        string // configured base URL; only its scheme and host are ever shown
	Path       string // fixed GET path, e.g. /health
}

// CmdRunner runs a fixed command and returns stdout. Tests replace it with a fake; nil runs it for real.
type CmdRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

const probeTimeout = 3 * time.Second

type healthJSON struct {
	Name       string `json:"name"`
	Capability string `json:"capability"`
	Endpoint   string `json:"endpoint"`
	// State is "up", "down" or "unavailable" (not configured or not an allowed local address).
	State     string `json:"state"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
	HTTPCode  int    `json:"http_status,omitempty"`
	// Reason is a fixed code (timeout, unreachable, http_error, redirect, not_configured, not_local), never raw error text.
	Reason    string    `json:"reason,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// loopbackBase returns scheme://host[:port] when raw is an http(s) URL with no credentials whose host
// is localhost or a loopback IP. Anything else is refused, so a probe can never leave this machine.
func loopbackBase(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func probe(ctx context.Context, client *http.Client, t HealthTarget) healthJSON {
	out := healthJSON{Name: t.Name, Capability: string(t.Capability), CheckedAt: time.Now().UTC()}
	if t.URL == "" {
		out.State, out.Reason = "unavailable", "not_configured"
		return out
	}
	base, ok := loopbackBase(t.URL)
	if !ok {
		out.State, out.Reason = "unavailable", "not_local"
		return out
	}
	out.Endpoint = base
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+t.Path, nil)
	if err != nil {
		out.State, out.Reason = "unavailable", "not_configured"
		return out
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		out.State, out.Reason = "down", "unreachable"
		if ctx.Err() != nil {
			out.Reason = "timeout"
		}
		return out
	}
	resp.Body.Close() // the body is never read or returned
	ms := time.Since(start).Milliseconds()
	out.LatencyMS, out.HTTPCode = &ms, resp.StatusCode
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		out.State, out.Reason = "down", "redirect"
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out.State = "up"
	default:
		out.State, out.Reason = "down", "http_error"
	}
	return out
}

// providerHealth probes every configured local service once, in parallel. It starts no GPU work.
func (s *Server) providerHealth(c echo.Context) error {
	client := providers.NewLocalClient(probeTimeout)
	res := make([]healthJSON, len(s.Health))
	done := make(chan struct{}, len(s.Health))
	for i, t := range s.Health {
		go func() { res[i] = probe(c.Request().Context(), client, t); done <- struct{}{} }()
	}
	for range s.Health {
		<-done
	}
	return c.JSON(http.StatusOK, map[string]any{"providers": res, "cloud_enabled": s.CloudEnabled})
}

type gpuJSON struct {
	Available    bool   `json:"available"`
	Name         string `json:"name,omitempty"`
	UtilPct      *int64 `json:"util_pct,omitempty"`
	TempC        *int64 `json:"temp_c,omitempty"`
	VRAMUsedMiB  *int64 `json:"vram_used_mib,omitempty"`
	VRAMTotalMiB *int64 `json:"vram_total_mib,omitempty"`
}

// readGPU asks nvidia-smi (fixed argument array, read-only query) for the first GPU. Any failure or
// unparsable field leaves it unavailable or that field null; nothing is guessed.
func readGPU(ctx context.Context, run CmdRunner) gpuJSON {
	if run == nil {
		run = runCmd
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := run(ctx, "nvidia-smi", "--query-gpu=name,utilization.gpu,temperature.gpu,memory.used,memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return gpuJSON{}
	}
	f := strings.Split(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), ",")
	if len(f) != 5 {
		return gpuJSON{}
	}
	num := func(s string) *int64 {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	return gpuJSON{Available: true, Name: strings.TrimSpace(f[0]), UtilPct: num(f[1]), TempC: num(f[2]), VRAMUsedMiB: num(f[3]), VRAMTotalMiB: num(f[4])}
}

type diskJSON struct {
	Name    string   `json:"name"`
	FreeGiB *float64 `json:"free_gib"`
	TotGiB  *float64 `json:"total_gib"`
}

func diskUsage(name, path string) diskJSON {
	d := diskJSON{Name: name}
	var fs syscall.Statfs_t
	if path == "" || syscall.Statfs(path, &fs) != nil {
		return d
	}
	free, tot := float64(fs.Bavail)*float64(fs.Bsize)/(1<<30), float64(fs.Blocks)*float64(fs.Bsize)/(1<<30)
	d.FreeGiB, d.TotGiB = &free, &tot
	return d
}

type queueJSON struct {
	Queued             int `json:"queued"`
	Running            int `json:"running"`
	WaitingForResource int `json:"waiting_for_resource"`
	RetryScheduled     int `json:"retry_scheduled"`
}

type slotJSON struct {
	Max     int     `json:"max_concurrent"`
	Running int     `json:"running"`
	Waiting int     `json:"waiting"`
	Stage   *string `json:"stage"`
	Episode *string `json:"episode"`
}

// monitor is a read-only snapshot: GPU via nvidia-smi, free space of the data and export directories,
// the queue and the GPU slot. It has no write path and cannot start work.
func (s *Server) monitor(c echo.Context) error {
	ctx := c.Request().Context()
	var q queueJSON
	var slot slotJSON
	err := s.DB.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM jobs WHERE state = 'queued'),
		(SELECT count(*) FROM jobs WHERE state = 'running'),
		(SELECT count(*) FROM jobs WHERE state = 'waiting_for_resource'),
		(SELECT count(*) FROM jobs WHERE state = 'retry_scheduled'),
		COALESCE((SELECT max_concurrent FROM resource_limits WHERE resource = 'gpu'), 1),
		(SELECT count(*) FROM jobs WHERE resource = 'gpu' AND state = 'running'),
		(SELECT count(*) FROM jobs WHERE resource = 'gpu' AND state = 'waiting_for_resource'),
		(SELECT j.stage FROM jobs j WHERE j.resource = 'gpu' AND j.state = 'running' ORDER BY j.id LIMIT 1),
		(SELECT e.title FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN episodes e ON e.id = r.episode_id
			WHERE j.resource = 'gpu' AND j.state = 'running' ORDER BY j.id LIMIT 1)`).
		Scan(&q.Queued, &q.Running, &q.WaitingForResource, &q.RetryScheduled, &slot.Max, &slot.Running, &slot.Waiting, &slot.Stage, &slot.Episode)
	if err != nil {
		return err
	}
	dataDir, _ := s.Store.Abs(".")
	return c.JSON(http.StatusOK, map[string]any{
		"gpu":   readGPU(ctx, s.RunCmd),
		"disks": []diskJSON{diskUsage("data", dataDir), diskUsage("exports", s.ExportDir)},
		"queue": q, "gpu_slot": slot,
	})
}
