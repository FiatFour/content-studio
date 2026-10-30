package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth/authtest"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

func healthServer(t *testing.T, mut func(*api.Server)) *httptest.Server {
	t.Helper()
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, ExportDir: t.TempDir(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Auth = authtest.Service(t, db)
	mut(s)
	ts := httptest.NewServer(authtest.AutoLogin(s.Echo(), authtest.Session(t, s.Auth)))
	t.Cleanup(ts.Close)
	return ts
}

func TestProviderHealthProbesOnlyLocalReadOnly(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer up.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	var hit atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1) }))
	defer elsewhere.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, elsewhere.URL, 302) }))
	defer redir.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	ts := healthServer(t, func(s *api.Server) {
		s.Health = []api.HealthTarget{
			{Name: "up", Capability: providers.CapLLM, URL: up.URL, Path: "/health"},
			{Name: "bad", Capability: providers.CapImage, URL: bad.URL, Path: "/x"},
			{Name: "redir", Capability: providers.CapTTS, URL: redir.URL, Path: "/x"},
			{Name: "dead", Capability: providers.CapTTS, URL: deadURL, Path: "/x"},
			{Name: "remote", Capability: providers.CapLLM, URL: "http://example.com", Path: "/x"},
			{Name: "creds", Capability: providers.CapLLM, URL: "http://user:s3cret@127.0.0.1:1", Path: "/x"},
			{Name: "none", Capability: providers.CapLLM, URL: "", Path: "/x"},
		}
	})
	resp, err := http.Get(ts.URL + "/api/providers/health")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "/health\"") {
		t.Errorf("secret or path leaked: %s", raw)
	}
	code, body := call(t, ts, "GET", "/api/providers/health", "", "")
	if code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	got := map[string]map[string]any{}
	for _, p := range body["providers"].([]any) {
		m := p.(map[string]any)
		got[m["name"].(string)] = m
	}
	want := map[string][2]string{"up": {"up", ""}, "bad": {"down", "http_error"}, "redir": {"down", "redirect"}, "dead": {"down", "unreachable"},
		"remote": {"unavailable", "not_local"}, "creds": {"unavailable", "not_local"}, "none": {"unavailable", "not_configured"}}
	for n, w := range want {
		r, _ := got[n]["reason"].(string)
		if got[n]["state"] != w[0] || r != w[1] {
			t.Errorf("%s: %v, want %v", n, got[n], w)
		}
	}
	if hit.Load() != 0 {
		t.Error("a redirect was followed")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range methods {
		if m != "GET" {
			t.Errorf("methods %v", methods)
		}
	}
}

func TestMonitorReadsGPUDisksAndQueue(t *testing.T) {
	var args []string
	ts := healthServer(t, func(s *api.Server) {
		s.RunCmd = func(ctx context.Context, name string, a ...string) ([]byte, error) {
			args = append([]string{name}, a...)
			return []byte("NVIDIA GeForce RTX 5060 Laptop GPU, 37, 52, 1024, 8151\n"), nil
		}
	})
	code, body := call(t, ts, "GET", "/api/monitor", "", "")
	if code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if len(args) != 3 || args[0] != "nvidia-smi" {
		t.Errorf("command %v", args)
	}
	gpu := body["gpu"].(map[string]any)
	if gpu["available"] != true || gpu["vram_total_mib"] != float64(8151) || gpu["util_pct"] != float64(37) || gpu["temp_c"] != float64(52) {
		t.Errorf("gpu %v", gpu)
	}
	disks := body["disks"].([]any)
	if len(disks) != 2 {
		t.Fatalf("disks %v", disks)
	}
	for _, d := range disks {
		if d.(map[string]any)["free_gib"] == nil {
			t.Errorf("disk not measured: %v", d)
		}
	}
	if _, ok := body["queue"].(map[string]any)["waiting_for_resource"]; !ok {
		t.Errorf("queue %v", body["queue"])
	}
	if body["gpu_slot"].(map[string]any)["max_concurrent"] != float64(1) {
		t.Errorf("slot %v", body["gpu_slot"])
	}
}

func TestMonitorGPUUnavailableIsNeverFaked(t *testing.T) {
	for name, run := range map[string]api.CmdRunner{
		"missing": func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not found") },
		"garbage": func(context.Context, string, ...string) ([]byte, error) { return []byte("oops"), nil },
	} {
		ts := healthServer(t, func(s *api.Server) { s.RunCmd = run })
		_, body := call(t, ts, "GET", "/api/monitor", "", "")
		gpu := body["gpu"].(map[string]any)
		if gpu["available"] != false || gpu["vram_total_mib"] != nil || gpu["util_pct"] != nil {
			t.Errorf("%s: %v", name, gpu)
		}
	}
}
