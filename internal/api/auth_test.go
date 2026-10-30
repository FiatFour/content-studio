package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth"
	"github.com/fiat/content-studio/internal/auth/authtest"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// rig is an API server with real authentication (no AutoLogin) and captured logs.
type rig struct {
	ts   *httptest.Server
	db   *pgxpool.Pool
	svc  *auth.Service
	logs *syncBuf
}

func newRig(t *testing.T) *rig { return newRigCfg(t, nil) }

// newRigCfg builds the rig with a custom auth config (nil means the localhost test config).
func newRigCfg(t *testing.T, cfg *auth.Config) *rig {
	t.Helper()
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	logs := &syncBuf{}
	svc := authtest.Service(t, db)
	if cfg != nil {
		var err error
		if svc, err = auth.New(db, *cfg); err != nil {
			t.Fatal(err)
		}
	}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, Auth: svc,
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	ts := httptest.NewServer(s.Echo())
	t.Cleanup(ts.Close)
	return &rig{ts: ts, db: db, svc: svc, logs: logs}
}

type resp struct {
	code   int
	body   []byte
	header http.Header
}

func (r resp) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.body, &m)
	return m
}

type opt struct {
	token  string
	origin string // "" means use the allowed origin; "-" means send none
	hdr    map[string]string
}

func (g *rig) do(t *testing.T, method, path, body string, o opt) resp {
	t.Helper()
	req, err := http.NewRequest(method, g.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	switch o.origin {
	case "":
		req.Header.Set("Origin", authtest.Origin)
	case "-":
	default:
		req.Header.Set("Origin", o.origin)
	}
	if o.token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: o.token})
	}
	for k, v := range o.hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, b, r.Header}
}

func (g *rig) sessionToken(t *testing.T) string { return authtest.Session(t, g.svc) }

// The authorization matrix: every route registered under /api, method and path, with a body that is
// valid enough to pass validation when a session exists. Public routes are listed separately.
var protectedRoutes = []struct{ method, path, body string }{
	{"GET", "/api/system", ""},
	{"GET", "/api/settings", ""},
	{"PUT", "/api/settings", `{"ui_lang":"en"}`},
	{"GET", "/api/episodes", ""},
	{"POST", "/api/episodes", `{"title":"t","idea":"an idea that is long enough","target_duration_s":60}`},
	{"GET", "/api/episodes/1", ""},
	{"PUT", "/api/episodes/1/summary", `{}`},
	{"PUT", "/api/episodes/1/in-story-date", `{}`},
	{"POST", "/api/episodes/1/rerender", ""},
	{"GET", "/api/runs/1/artifacts", ""},
	{"POST", "/api/runs/1/cancel", ""},
	{"POST", "/api/runs/1/retry", ""},
	{"GET", "/api/artifacts/1/content", ""},
	{"GET", "/api/videos/1/review", ""},
	{"POST", "/api/videos/1/decision", `{"decision":"approved","sha256":"x"}`},
	{"POST", "/api/videos/1/export", ""},
	{"GET", "/api/characters", ""},
	{"POST", "/api/characters", `{}`},
	{"PUT", "/api/characters/1", `{}`},
	{"GET", "/api/audit", ""},
	{"GET", "/api/notifications", ""},
	{"PUT", "/api/notifications/events/job_failed", `{"enabled":true}`},
	{"POST", "/api/notifications/test", ""},
	{"GET", "/api/resources", ""},
	{"PUT", "/api/resources/gpu", `{"max_concurrent":2,"confirm_unsafe":true}`},
	{"GET", "/api/policies", ""},
	{"GET", "/api/auth/credentials", ""},
	{"DELETE", "/api/auth/credentials/1", ""},
	{"POST", "/api/auth/logout", ""},   // keep last among real routes: it revokes the session
	{"GET", "/api/does-not-exist", ""}, // default deny also covers paths that do not exist
	{"POST", "/api/does-not-exist", `{}`},
}

func TestEverySensitiveRouteRefusesWithoutSession(t *testing.T) {
	g := newRig(t)
	for _, rt := range protectedRoutes {
		for name, o := range map[string]opt{
			"no cookie":       {},
			"garbage cookie":  {token: "not-a-real-token"},
			"bearer header":   {hdr: map[string]string{"Authorization": "Bearer abc", "X-Actor": "owner"}},
			"claims owner":    {hdr: map[string]string{"X-Forwarded-User": "owner"}},
			"origin-less get": {origin: "-"},
		} {
			r := g.do(t, rt.method, rt.path, rt.body, o)
			// An Origin-less state-changing request is refused earlier (403); everything else is 401.
			want := http.StatusUnauthorized
			if name == "origin-less get" && rt.method != "GET" {
				want = http.StatusForbidden
			}
			if r.code != want {
				t.Errorf("%s %s (%s): %d, want %d", rt.method, rt.path, name, r.code, want)
			}
			if strings.Contains(string(r.body), "session") || strings.Contains(string(r.body), "expired") {
				t.Errorf("%s %s: refusal explains itself: %s", rt.method, rt.path, r.body)
			}
		}
	}
}

func TestEverySensitiveRouteWorksWithSession(t *testing.T) {
	g := newRig(t)
	tok := g.sessionToken(t)
	for _, rt := range protectedRoutes {
		if strings.Contains(rt.path, "does-not-exist") {
			continue
		}
		r := g.do(t, rt.method, rt.path, rt.body, opt{token: tok})
		if r.code == http.StatusUnauthorized || r.code == http.StatusForbidden || r.code >= 500 {
			t.Errorf("%s %s with session: %d %s", rt.method, rt.path, r.code, r.body)
		}
	}
	// Notifications are not configured on this rig: GET answers 404 (route absent) but never 401.
}

func TestPublicRoutesNeedNoSession(t *testing.T) {
	g := newRig(t)
	if r := g.do(t, "GET", "/api/health", "", opt{}); r.code != 200 {
		t.Fatalf("health %d", r.code)
	}
	r := g.do(t, "GET", "/api/auth/status", "", opt{})
	if r.code != 200 || r.json()["authenticated"] != false || r.json()["enrolled"] != false {
		t.Fatalf("status %d %s", r.code, r.body)
	}
	if r := g.do(t, "POST", "/api/auth/login/begin", "", opt{}); r.code != 200 {
		t.Fatalf("login begin %d %s", r.code, r.body)
	}
	tok := g.sessionToken(t)
	if r := g.do(t, "GET", "/api/auth/status", "", opt{token: tok}); r.json()["authenticated"] != true {
		t.Fatalf("status with session %s", r.body)
	}
}

func TestServerWithoutAuthServiceFailsClosed(t *testing.T) {
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(s.Echo())
	defer ts.Close()
	for _, p := range []string{"/api/episodes", "/api/settings", "/api/audit"} {
		r, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatalf("%s: %d", p, r.StatusCode)
		}
	}
	req, _ := http.NewRequest("PUT", ts.URL+"/api/settings", strings.NewReader(`{"ui_lang":"en"}`))
	req.Header.Set("Origin", authtest.Origin)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode == 200 {
		t.Fatal("settings written without an auth service")
	}
}

func TestExpiredRevokedAndTamperedSessionsAreRefused(t *testing.T) {
	g := newRig(t)
	get := func(tok string) int { return g.do(t, "GET", "/api/episodes", "", opt{token: tok}).code }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := g.db.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	ok := g.sessionToken(t)
	if get(ok) != 200 {
		t.Fatal("fresh session refused")
	}
	abs := g.sessionToken(t)
	exec(`UPDATE auth_sessions SET expires_at = now() - interval '1 second' WHERE token_hash = $1`, sha(abs))
	if get(abs) != 401 {
		t.Fatal("absolute-expired session accepted")
	}
	idle := g.sessionToken(t)
	exec(`UPDATE auth_sessions SET last_seen_at = now() - interval '13 hours' WHERE token_hash = $1`, sha(idle))
	if get(idle) != 401 {
		t.Fatal("idle-expired session accepted")
	}
	rev := g.sessionToken(t)
	exec(`UPDATE auth_sessions SET revoked_at = now() WHERE token_hash = $1`, sha(rev))
	if get(rev) != 401 {
		t.Fatal("revoked session accepted")
	}
	tampered := []byte(ok)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}
	if get(string(tampered)) != 401 || get(ok+"x") != 401 || get(ok[:len(ok)-1]) != 401 {
		t.Fatal("tampered session accepted")
	}
	if get(ok) != 200 {
		t.Fatal("valid session stopped working")
	}
}

func sha(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func TestSessionTokenIsNotStoredInPlaintext(t *testing.T) {
	g := newRig(t)
	tok := g.sessionToken(t)
	var n int
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM auth_sessions WHERE token_hash = $1`, sha(tok)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("hash lookup: %d %v", n, err)
	}
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM auth_sessions s WHERE s::text LIKE '%' || $1 || '%'`, tok).Scan(&n); err != nil || n != 0 {
		t.Fatalf("token appears in the table: %d %v", n, err)
	}
}

func TestOriginGuardOnStateChangingRequests(t *testing.T) {
	g := newRig(t)
	tok := g.sessionToken(t)
	body := `{"ui_lang":"en"}`
	for name, o := range map[string]opt{
		"no origin":      {token: tok, origin: "-"},
		"foreign origin": {token: tok, origin: "https://evil.example"},
		"null origin":    {token: tok, origin: "null"},
		"prefix trick":   {token: tok, origin: authtest.Origin + ".evil.example"},
		"cross-site":     {token: tok, hdr: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"same-site":      {token: tok, hdr: map[string]string{"Sec-Fetch-Site": "same-site"}},
	} {
		if r := g.do(t, "PUT", "/api/settings", body, o); r.code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, r.code)
		}
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if r := g.do(t, m, "/api/does-not-exist", "", opt{token: tok, origin: "https://evil.example"}); r.code != 403 {
			t.Errorf("%s with foreign origin: %d", m, r.code)
		}
	}
	if r := g.do(t, "PUT", "/api/settings", body, opt{token: tok, hdr: map[string]string{"Sec-Fetch-Site": "same-origin"}}); r.code != 200 {
		t.Fatalf("legitimate request refused: %d %s", r.code, r.body)
	}
	if r := g.do(t, "GET", "/api/settings", "", opt{token: tok, origin: "-"}); r.code != 200 {
		t.Fatalf("GET without Origin refused: %d", r.code)
	}
	var lang string
	if err := g.db.QueryRow(context.Background(), `SELECT ui_lang FROM user_settings`).Scan(&lang); err != nil || lang != "en" {
		t.Fatalf("db %q %v", lang, err)
	}
}

// ceremonyResult splits a begin response.
func ceremonyOf(t *testing.T, r resp) (string, json.RawMessage) {
	t.Helper()
	var v struct {
		ID      string          `json:"ceremony_id"`
		Options json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(r.body, &v); err != nil || v.ID == "" {
		t.Fatalf("begin: %d %s", r.code, r.body)
	}
	return v.ID, v.Options
}

func finishBody(id, label string, cred json.RawMessage) string {
	b, _ := json.Marshal(map[string]any{"ceremony_id": id, "label": label, "credential": cred})
	return string(b)
}

func beginBody(code string) string {
	b, _ := json.Marshal(map[string]string{"code": code})
	return string(b)
}

// enrollFirst runs the full first-passkey ceremony and returns the authenticator and the session cookie.
func enrollFirst(t *testing.T, g *rig) (*authtest.Authenticator, string, string) {
	t.Helper()
	code, _, err := g.svc.NewEnrollCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := g.do(t, "POST", "/api/auth/enroll/begin", beginBody(code), opt{})
	if r.code != 200 {
		t.Fatalf("enroll begin %d %s", r.code, r.body)
	}
	id, options := ceremonyOf(t, r)
	au := authtest.NewAuthenticator(t, "localhost", authtest.Origin)
	r = g.do(t, "POST", "/api/auth/enroll/finish", finishBody(id, "Laptop", au.Create(options)), opt{})
	if r.code != 201 {
		t.Fatalf("enroll finish %d %s", r.code, r.body)
	}
	return au, cookieValue(t, r), code
}

func cookieValue(t *testing.T, r resp) string {
	t.Helper()
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie set")
	return ""
}

func TestEnrollmentLoginAndLogoutWithSoftwarePasskey(t *testing.T) {
	g := newRig(t)
	au, enrollTok, code := enrollFirst(t, g)

	// The first enrollment logs the owner in; the cookie is HttpOnly, SameSite=Strict, not Secure on http.
	r := g.do(t, "GET", "/api/episodes", "", opt{token: enrollTok})
	if r.code != 200 {
		t.Fatalf("session from enrollment: %d", r.code)
	}
	r = g.do(t, "GET", "/api/auth/status", "", opt{})
	if r.json()["enrolled"] != true || r.json()["authenticated"] != false {
		t.Fatalf("status %s", r.body)
	}

	// Login with a real signature.
	r = g.do(t, "POST", "/api/auth/login/begin", "", opt{})
	id, options := ceremonyOf(t, r)
	r = g.do(t, "POST", "/api/auth/login/finish", finishBody(id, "", au.Get(options, false)), opt{})
	if r.code != 200 {
		t.Fatalf("login %d %s", r.code, r.body)
	}
	var ck *http.Cookie
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == auth.CookieName {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Secure || ck.Path != "/" || ck.MaxAge <= 0 {
		t.Fatalf("cookie attributes: %+v", ck)
	}
	tok := ck.Value
	if g.do(t, "GET", "/api/audit", "", opt{token: tok}).code != 200 {
		t.Fatal("session from login refused")
	}

	// A signature that does not verify, and a replayed ceremony, fail.
	r = g.do(t, "POST", "/api/auth/login/begin", "", opt{})
	id, options = ceremonyOf(t, r)
	if r := g.do(t, "POST", "/api/auth/login/finish", finishBody(id, "", au.Get(options, true)), opt{}); r.code != 401 {
		t.Fatalf("bad signature: %d", r.code)
	}
	r = g.do(t, "POST", "/api/auth/login/begin", "", opt{})
	id, options = ceremonyOf(t, r)
	good := au.Get(options, false)
	if r := g.do(t, "POST", "/api/auth/login/finish", finishBody(id, "", good), opt{}); r.code != 200 {
		t.Fatalf("second login: %d", r.code)
	}
	if r := g.do(t, "POST", "/api/auth/login/finish", finishBody(id, "", good), opt{}); r.code != 401 {
		t.Fatalf("replayed ceremony: %d", r.code)
	}

	// A counter that goes backwards means a cloned passkey: refused.
	au.Count = 0
	r = g.do(t, "POST", "/api/auth/login/begin", "", opt{})
	id, options = ceremonyOf(t, r)
	if r := g.do(t, "POST", "/api/auth/login/finish", finishBody(id, "", au.Get(options, false)), opt{}); r.code != 401 {
		t.Fatalf("clone warning not refused: %d", r.code)
	}

	// Logout revokes the session server-side and clears the cookie.
	r = g.do(t, "POST", "/api/auth/logout", "", opt{token: tok})
	if r.code != 200 {
		t.Fatalf("logout %d", r.code)
	}
	if c := (&http.Response{Header: r.header}).Cookies(); len(c) == 0 || c[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %v", c)
	}
	if g.do(t, "GET", "/api/episodes", "", opt{token: tok}).code != 401 {
		t.Fatal("session usable after logout")
	}

	// Audit trail of the auth events, with the passkey as actor and no secrets.
	rows, err := g.db.Query(context.Background(), `SELECT action, actor FROM audit_log WHERE action LIKE 'auth.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for rows.Next() {
		var a, actor string
		if err := rows.Scan(&a, &actor); err != nil {
			t.Fatal(err)
		}
		seen[a] = actor
	}
	for _, a := range []string{"auth.enroll_code.create", "auth.enroll", "auth.login.success", "auth.login.failure", "auth.logout"} {
		if _, ok := seen[a]; !ok {
			t.Errorf("no audit row %s (have %v)", a, seen)
		}
	}
	if !strings.HasPrefix(seen["auth.logout"], "owner:passkey-") || seen["auth.login.failure"] != "anonymous" {
		t.Errorf("actors %v", seen)
	}
	g.assertNoSecrets(t, code, enrollTok, tok)
}

// assertNoSecrets: none of the secrets appear in the logs or in any audit row.
func (g *rig) assertNoSecrets(t *testing.T, secrets ...string) {
	t.Helper()
	var audit string
	if err := g.db.QueryRow(context.Background(), `SELECT coalesce(string_agg(a::text, E'\n'), '') FROM audit_log a`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	logs := g.logs.String()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		raw := strings.ReplaceAll(s, "-", "")
		for what, hay := range map[string]string{"logs": logs, "audit": audit} {
			if strings.Contains(hay, s) || strings.Contains(hay, raw) {
				t.Errorf("secret %q... leaked into %s", s[:4], what)
			}
		}
	}
}

func TestAudienceCannotAddPasskeyWithoutSessionOrCode(t *testing.T) {
	g := newRig(t)

	// Fresh install: no code, wrong code, expired code and a used code all fail; calling the API alone is not enough.
	if r := g.do(t, "POST", "/api/auth/enroll/begin", beginBody(""), opt{}); r.code != 401 {
		t.Fatalf("no code: %d", r.code)
	}
	if r := g.do(t, "POST", "/api/auth/enroll/begin", beginBody("AAAAA-BBBBB-CCCCC-DDDDD"), opt{}); r.code != 401 {
		t.Fatalf("wrong code: %d", r.code)
	}
	code, _, err := g.svc.NewEnrollCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var plain int
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM auth_enroll_codes c WHERE c::text LIKE '%' || $1 || '%'`, strings.ReplaceAll(code, "-", "")).Scan(&plain); err != nil || plain != 0 {
		t.Fatalf("enrollment code stored in plaintext: %d %v", plain, err)
	}
	if _, err := g.db.Exec(context.Background(), `UPDATE auth_enroll_codes SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if r := g.do(t, "POST", "/api/auth/enroll/begin", beginBody(code), opt{}); r.code != 401 {
		t.Fatalf("expired code: %d", r.code)
	}
	if _, err := g.db.Exec(context.Background(), `UPDATE auth_enroll_codes SET expires_at = now() + interval '5 minutes'`); err != nil {
		t.Fatal(err)
	}
	// Two ceremonies from the same code; only one can finish (single use).
	var ids [2]string
	var opts [2]json.RawMessage
	for i := range ids {
		ids[i], opts[i] = ceremonyOf(t, g.do(t, "POST", "/api/auth/enroll/begin", beginBody(strings.ToLower(code)), opt{}))
	}
	a1 := authtest.NewAuthenticator(t, "localhost", authtest.Origin)
	a2 := authtest.NewAuthenticator(t, "localhost", authtest.Origin)
	if r := g.do(t, "POST", "/api/auth/enroll/finish", finishBody(ids[0], "one", a1.Create(opts[0])), opt{}); r.code != 201 {
		t.Fatalf("first finish: %d %s", r.code, r.body)
	}
	if r := g.do(t, "POST", "/api/auth/enroll/finish", finishBody(ids[1], "two", a2.Create(opts[1])), opt{}); r.code != 401 {
		t.Fatalf("code reused: %d %s", r.code, r.body)
	}
	assertCreds(t, g, 1)

	// With a passkey present, no code can add another, even a leaked valid one; no new code can be made.
	if _, _, err := g.svc.NewEnrollCode(context.Background()); err != auth.ErrAlreadyEnrolled {
		t.Fatalf("NewEnrollCode with a credential: %v", err)
	}
	if _, err := g.db.Exec(context.Background(), `INSERT INTO auth_enroll_codes (code_hash, expires_at) VALUES ($1, now() + interval '5 minutes')`, sha("LEAKEDLEAKEDLEAKEDLE")); err != nil {
		t.Fatal(err)
	}
	if r := g.do(t, "POST", "/api/auth/enroll/begin", beginBody("LEAKED-LEAKE-DLEAK-EDLE"), opt{}); r.code != 403 {
		t.Fatalf("second credential without session: %d %s", r.code, r.body)
	}
	assertCreds(t, g, 1)
}

func assertCreds(t *testing.T, g *rig, want int) {
	t.Helper()
	var n int
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM auth_credentials`).Scan(&n); err != nil || n != want {
		t.Fatalf("credentials: %d, want %d (%v)", n, want, err)
	}
}

func TestAddAndRemovePasskeysWithSession(t *testing.T) {
	g := newRig(t)
	_, tok, _ := enrollFirst(t, g)

	r := g.do(t, "POST", "/api/auth/enroll/begin", "{}", opt{token: tok})
	id, options := ceremonyOf(t, r)
	second := authtest.NewAuthenticator(t, "localhost", authtest.Origin)
	cred := second.Create(options)
	// A different (or no) session cannot finish a ceremony it did not start.
	if r := g.do(t, "POST", "/api/auth/enroll/finish", finishBody(id, "x", cred), opt{token: g.sessionToken(t)}); r.code != 401 {
		t.Fatalf("foreign session finished the ceremony: %d", r.code)
	}
	r = g.do(t, "POST", "/api/auth/enroll/begin", "{}", opt{token: tok})
	id, options = ceremonyOf(t, r)
	cred = second.Create(options)
	if r := g.do(t, "POST", "/api/auth/enroll/finish", finishBody(id, "Phone", cred), opt{}); r.code != 401 {
		t.Fatalf("finish without session: %d", r.code)
	}
	r = g.do(t, "POST", "/api/auth/enroll/begin", "{}", opt{token: tok})
	id, options = ceremonyOf(t, r)
	if r := g.do(t, "POST", "/api/auth/enroll/finish", finishBody(id, "Phone", second.Create(options)), opt{token: tok}); r.code != 201 {
		t.Fatalf("add passkey: %d %s", r.code, r.body)
	}
	assertCreds(t, g, 2)

	list := g.do(t, "GET", "/api/auth/credentials", "", opt{token: tok}).json()
	creds := list["credentials"].([]any)
	if len(creds) != 2 || strings.Contains(string(mustJSON(list)), "public") {
		t.Fatalf("list %v", list)
	}
	if g.do(t, "DELETE", "/api/auth/credentials/999", "", opt{token: tok}).code != 404 {
		t.Fatal("deleting an unknown id not 404")
	}
	first := int64(creds[0].(map[string]any)["id"].(float64))
	secondID := int64(creds[1].(map[string]any)["id"].(float64))
	if r := g.do(t, "DELETE", "/api/auth/credentials/"+itoa(secondID), "", opt{token: tok}); r.code != 200 {
		t.Fatalf("remove: %d %s", r.code, r.body)
	}
	if r := g.do(t, "DELETE", "/api/auth/credentials/"+itoa(first), "", opt{token: tok}); r.code != 409 {
		t.Fatalf("removing the last passkey: %d", r.code)
	}
	assertCreds(t, g, 1)
	var n int
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action IN ('auth.passkey.add','auth.passkey.remove')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("passkey audit rows: %d %v", n, err)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func itoa(n int64) string { return strings.TrimSpace(string(mustJSON(n))) }

func TestFailedEnrollmentAttemptsAreRateLimited(t *testing.T) {
	g := newRig(t)
	var last int
	for i := 0; i < 12; i++ {
		last = g.do(t, "POST", "/api/auth/enroll/begin", beginBody("AAAAA-BBBBB-CCCCC-DDDDD"), opt{}).code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 12 bad codes: %d, want 429", last)
	}
	// Login is locked out the same way (shared per-IP counter), and a lockout does not leak the reason.
	if r := g.do(t, "POST", "/api/auth/login/begin", "", opt{}); r.code != 429 {
		t.Fatalf("login during lockout: %d", r.code)
	}
}

func TestErrorBodiesCarryNoSecrets(t *testing.T) {
	g := newRig(t)
	code, _, err := g.svc.NewEnrollCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var bodies strings.Builder
	for _, b := range []string{beginBody(code[:10]), `not json`, beginBody(code + "x")} {
		bodies.Write(g.do(t, "POST", "/api/auth/enroll/begin", b, opt{}).body)
	}
	bodies.Write(g.do(t, "POST", "/api/auth/enroll/finish", `{"ceremony_id":"x","credential":{}}`, opt{}).body)
	bodies.Write(g.do(t, "POST", "/api/auth/login/finish", `{"ceremony_id":"x","credential":{}}`, opt{token: code}).body)
	if strings.Contains(bodies.String(), strings.ReplaceAll(code, "-", "")) || strings.Contains(bodies.String(), code) {
		t.Fatalf("enrollment code in an error body: %s", bodies.String())
	}
	g.assertNoSecrets(t, code)
}

func TestCookieIsSecureBehindConfiguredHTTPSOrigin(t *testing.T) {
	const origin = "https://studio.example"
	g := newRigCfg(t, &auth.Config{RPID: "studio.example", Origins: []string{origin}})
	code, _, err := g.svc.NewEnrollCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := g.do(t, "POST", "/api/auth/enroll/begin", beginBody(code), opt{origin: origin})
	id, options := ceremonyOf(t, r)
	au := authtest.NewAuthenticator(t, "studio.example", origin)
	r = g.do(t, "POST", "/api/auth/enroll/finish", finishBody(id, "", au.Create(options)), opt{origin: origin})
	if r.code != 201 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == auth.CookieName && c.Secure && c.HttpOnly && c.SameSite == http.SameSiteStrictMode {
			return
		}
	}
	t.Fatalf("cookie not Secure/HttpOnly/Strict: %v", r.header["Set-Cookie"])
}
