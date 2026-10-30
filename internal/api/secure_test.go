package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth"
	"github.com/fiat/content-studio/internal/auth/authtest"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// secRig is a server with a web dir, custom limits and optionally TLS.
type secRig struct {
	*rig
	svc *auth.Service
}

func newSecRig(t *testing.T, cfg *auth.Config, lim api.Limits, tls bool) *secRig {
	t.Helper()
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	svc := authtest.Service(t, db)
	if cfg != nil {
		var err error
		if svc, err = auth.New(db, *cfg); err != nil {
			t.Fatal(err)
		}
	}
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, Auth: svc, WebDir: web, Limits: lim,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var ts *httptest.Server
	if tls {
		ts = httptest.NewTLSServer(s.Echo())
	} else {
		ts = httptest.NewServer(s.Echo())
	}
	t.Cleanup(ts.Close)
	return &secRig{rig: &rig{ts: ts, db: db, svc: svc, logs: &syncBuf{}}, svc: svc}
}

var wantHeaders = []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy",
	"Permissions-Policy", "Cross-Origin-Opener-Policy", "Cross-Origin-Resource-Policy"}

func assertHeaders(t *testing.T, name string, h http.Header, hsts bool) {
	t.Helper()
	for _, k := range wantHeaders {
		if h.Get(k) == "" {
			t.Errorf("%s: missing %s", name, k)
		}
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("%s: weak values %v", name, h)
	}
	csp := h.Get("Content-Security-Policy")
	for _, must := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, must) {
			t.Errorf("%s: CSP lacks %q: %s", name, must, csp)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("%s: CSP allows script injection: %s", name, csp)
	}
	pp := h.Get("Permissions-Policy")
	if !strings.Contains(pp, "publickey-credentials-get=(self)") || !strings.Contains(pp, "publickey-credentials-create=(self)") || !strings.Contains(pp, "camera=()") {
		t.Errorf("%s: Permissions-Policy %q", name, pp)
	}
	if got := h.Get("Strict-Transport-Security") != ""; got != hsts {
		t.Errorf("%s: HSTS present=%v, want %v", name, got, hsts)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{}, false)
	tok := g.sessionToken(t)
	for name, c := range map[string]struct {
		method, path string
		o            opt
	}{
		"api ok":          {"GET", "/api/health", opt{}},
		"api authed":      {"GET", "/api/settings", opt{token: tok}},
		"api 401":         {"GET", "/api/episodes", opt{}},
		"api 404 authed":  {"GET", "/api/nope", opt{token: tok}},
		"api 403 origin":  {"PUT", "/api/settings", opt{token: tok, origin: "https://evil.example"}},
		"spa index":       {"GET", "/", opt{}},
		"spa deep link":   {"GET", "/some/route", opt{}},
		"spa static file": {"GET", "/index.html", opt{}},
	} {
		r := g.do(t, c.method, c.path, "", c.o)
		assertHeaders(t, name, r.header, false)
	}
	if r := g.do(t, "GET", "/api/health", "", opt{}); r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("API response cache-control %q", r.header.Get("Cache-Control"))
	}
}

func TestHSTSOnlyForHTTPS(t *testing.T) {
	// Plain http on localhost: never.
	g := newSecRig(t, nil, api.Limits{}, false)
	assertHeaders(t, "http localhost", g.do(t, "GET", "/api/health", "", opt{}).header, false)

	// Direct TLS: yes.
	const origin = "https://studio.example"
	cfg := &auth.Config{RPID: "studio.example", Origins: []string{origin}}
	tl := newSecRig(t, cfg, api.Limits{}, true)
	req, _ := http.NewRequest("GET", tl.ts.URL+"/api/health", nil)
	resp, err := tl.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	assertHeaders(t, "tls", resp.Header, true)

	// Behind a TLS proxy: plain http to us, but the Host is a configured https origin's host: yes.
	px := newSecRig(t, cfg, api.Limits{}, false)
	req, _ = http.NewRequest("GET", px.ts.URL+"/api/health", nil)
	req.Host = "studio.example"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	assertHeaders(t, "proxied https host", resp.Header, true)
	// A different Host with the same server: no HSTS.
	req, _ = http.NewRequest("GET", px.ts.URL+"/api/health", nil)
	req.Host = "other.example"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	assertHeaders(t, "unrelated host", resp.Header, false)
}

func TestBodyLimitReturns413(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{MaxBody: 2048}, false)
	tok := g.sessionToken(t)
	big := `{"ui_lang":"en","pad":"` + strings.Repeat("a", 3000) + `"}`
	// Declared length over the limit.
	r := g.do(t, "POST", "/api/episodes", big, opt{token: tok})
	if r.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("global limit: %d %s", r.code, r.body)
	}
	assertHeaders(t, "413", r.header, false)
	// Per-endpoint override: settings take 16 KiB at most, but never more than the global limit.
	small := `{"ui_lang":"en","pad":"` + strings.Repeat("a", 1500) + `"}`
	if r := g.do(t, "PUT", "/api/settings", small, opt{token: tok}); r.code != 200 {
		t.Fatalf("under the limit refused: %d %s", r.code, r.body)
	}
	// The default global limit (1 MiB) with the small-endpoint cap (16 KiB) and the auth cap (64 KiB).
	d := newSecRig(t, nil, api.Limits{}, false)
	dtok := d.sessionToken(t)
	if r := d.do(t, "PUT", "/api/settings", `{"ui_lang":"en","pad":"`+strings.Repeat("a", 17<<10)+`"}`, opt{token: dtok}); r.code != 413 {
		t.Fatalf("settings 17 KiB: %d", r.code)
	}
	if r := d.do(t, "POST", "/api/auth/login/finish", `{"pad":"`+strings.Repeat("a", 65<<10)+`"}`, opt{}); r.code != 413 {
		t.Fatalf("auth 65 KiB: %d", r.code)
	}
	if r := d.do(t, "POST", "/api/episodes", `{"title":"t","pad":"`+strings.Repeat("a", 200<<10)+`"}`, opt{token: dtok}); r.code == 413 {
		t.Fatalf("200 KiB episode body refused under the 1 MiB default")
	}
	if r := d.do(t, "POST", "/api/episodes", `{"pad":"`+strings.Repeat("a", 1<<20)+`"}`, opt{token: dtok}); r.code != 413 {
		t.Fatalf("1 MiB+ body: %d", r.code)
	}
}

func TestChunkedBodyOverLimitReturns413(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{MaxBody: 2048}, false)
	tok := g.sessionToken(t)
	req, _ := http.NewRequest("POST", g.ts.URL+"/api/episodes", io.NopCloser(strings.NewReader(`{"pad":"`+strings.Repeat("a", 5000)+`"}`))) // unknown length: chunked
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", authtest.Origin)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked: %d", resp.StatusCode)
	}
}

func TestContentTypeEnforcedOnBodies(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{}, false)
	tok := g.sessionToken(t)
	body := `{"ui_lang":"en"}`
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonx", "application/xml"} {
		hdr := map[string]string{"Content-Type": ct}
		r := g.do(t, "PUT", "/api/settings", body, opt{token: tok, hdr: hdr})
		if r.code != http.StatusUnsupportedMediaType {
			t.Errorf("%q: %d, want 415", ct, r.code)
		}
	}
	// Missing content type entirely.
	req, _ := http.NewRequest("PUT", g.ts.URL+"/api/settings", strings.NewReader(body))
	req.Header.Set("Origin", authtest.Origin)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("no content type: %d", resp.StatusCode)
	}
	for _, ok := range []string{"application/json", "application/json; charset=utf-8"} {
		if r := g.do(t, "PUT", "/api/settings", body, opt{token: tok, hdr: map[string]string{"Content-Type": ok}}); r.code != 200 {
			t.Errorf("%q: %d %s", ok, r.code, r.body)
		}
	}
	// Bodyless state-changing calls need no content type.
	if r := g.do(t, "POST", "/api/runs/999/cancel", "", opt{token: tok, hdr: map[string]string{"Content-Type": "text/plain"}}); r.code == 415 {
		t.Errorf("bodyless POST refused for content type")
	}
	if sess := g.do(t, "GET", "/api/settings", "", opt{token: tok, hdr: map[string]string{"Content-Type": "text/plain"}}); sess.code != 200 {
		t.Errorf("GET refused for content type: %d", sess.code)
	}
}

func TestRateLimitReturns429WithRetryAfterAndSparesHealth(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{AnonPerMinute: 40, AuthPerMinute: 400}, false)
	tok := g.sessionToken(t)

	// Anonymous: burst is 10 for 40/min. The 11th request in a burst is refused.
	var limited resp
	n := 0
	for ; n < 30; n++ {
		r := g.do(t, "GET", "/api/auth/status", "", opt{})
		if r.code == 429 {
			limited = r
			break
		}
		if r.code != 200 {
			t.Fatalf("anon request %d: %d", n, r.code)
		}
	}
	if limited.code != 429 || n < 5 || n > 20 {
		t.Fatalf("anonymous requests: 429 after %d (want about the burst of 10)", n)
	}
	if ra, err := strconv.Atoi(limited.header.Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After %q", limited.header.Get("Retry-After"))
	}
	assertHeaders(t, "429", limited.header, false)
	if limited.json()["error"] == nil {
		t.Fatalf("429 body %s", limited.body)
	}

	// Health is never limited, even while the anonymous bucket is empty.
	for i := 0; i < 60; i++ {
		if r := g.do(t, "GET", "/api/health", "", opt{}); r.code != 200 {
			t.Fatalf("health limited at %d: %d", i, r.code)
		}
	}
	// The signed-in owner has a separate bucket and is not locked out by anonymous traffic.
	for i := 0; i < 50; i++ {
		if r := g.do(t, "GET", "/api/settings", "", opt{token: tok}); r.code != 200 {
			t.Fatalf("authenticated request %d: %d", i, r.code)
		}
	}
	// A forged cookie gets its own bucket, so it cannot drain the owner's either.
	for i := 0; i < 30; i++ {
		g.do(t, "GET", "/api/settings", "", opt{token: "forged-" + strconv.Itoa(i%2)})
	}
	if r := g.do(t, "GET", "/api/settings", "", opt{token: tok}); r.code != 200 {
		t.Fatalf("owner locked out by forged cookies: %d", r.code)
	}
	// The authenticated bucket also ends: 400/min has a burst of 100.
	got429 := false
	for i := 0; i < 150 && !got429; i++ {
		got429 = g.do(t, "GET", "/api/settings", "", opt{token: tok}).code == 429
	}
	if !got429 {
		t.Fatal("authenticated traffic never limited")
	}
}

func TestRateLimitDoesNotApplyToStaticFiles(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{AnonPerMinute: 10}, false)
	for i := 0; i < 40; i++ {
		if r := g.do(t, "GET", "/", "", opt{}); r.code != 200 {
			t.Fatalf("SPA request %d: %d", i, r.code)
		}
	}
}

func TestLoginFailureLimiterStillAppliesAlongsideGeneralLimit(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{AnonPerMinute: 100000}, false)
	code := 0
	for i := 0; i < 12 && code != 429; i++ {
		code = g.do(t, "POST", "/api/auth/enroll/begin", `{"code":"wrongwrongwrongwrong"}`, opt{}).code
	}
	if code != 429 {
		t.Fatalf("enrollment failures not limited (last %d)", code)
	}
}

func TestNoUploadEndpointsExist(t *testing.T) {
	g := newSecRig(t, nil, api.Limits{}, false)
	tok := g.sessionToken(t)
	// Every route that takes a body demands JSON, so a multipart upload is refused before any handler.
	for _, p := range []string{"/api/episodes", "/api/characters", "/api/artifacts/1/content", "/api/upload"} {
		r := g.do(t, "POST", p, "--x\r\n\r\n--x--", opt{token: tok, hdr: map[string]string{"Content-Type": "multipart/form-data; boundary=x"}})
		if r.code != 415 {
			t.Errorf("%s: multipart accepted with %d", p, r.code)
		}
	}
}
