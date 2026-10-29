package api_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth/authtest"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/notify"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

const hookURL = "https://discord.com/api/webhooks/987654321/APITOKENsecret-wxyz"

type toFake struct{ target *url.URL }

func (t toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// notifyServer wires a real notify.Service to a fake Discord receiver (nil means not configured).
func notifyServer(t *testing.T, configured bool, status int, posts *atomic.Int32) (*httptest.Server, *pgxpool.Pool, *artifacts.Store, *bytes.Buffer) {
	t.Helper()
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	ns := &notify.Service{DB: db, Log: log}
	if configured {
		fakeDiscord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			posts.Add(1)
			w.WriteHeader(status)
		}))
		t.Cleanup(fakeDiscord.Close)
		u, _ := url.Parse(fakeDiscord.URL)
		d, err := notify.NewDiscord(hookURL, &http.Client{Transport: toFake{u}})
		if err != nil {
			t.Fatal(err)
		}
		ns.Notifier, ns.Suffix = d, d.Suffix()
	}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, Log: log, Notify: ns}
	s.Auth = authtest.Service(t, db)
	ts := httptest.NewServer(authtest.AutoLogin(s.Echo(), authtest.Session(t, s.Auth)))
	t.Cleanup(ts.Close)
	return ts, db, st, &logs
}

func TestNotificationsNotConfigured(t *testing.T) {
	ts, _, _, _ := notifyServer(t, false, 0, nil)
	code, body := call(t, ts, "GET", "/api/notifications", "", "")
	if code != 200 || body["configured"] != false {
		t.Fatalf("%d %v", code, body)
	}
	if _, has := body["suffix"]; has {
		t.Fatal("suffix shown while not configured")
	}
	if code, body := call(t, ts, "POST", "/api/notifications/test", "", "{}"); code != http.StatusConflict || body["error"] != "not configured" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestNotificationsAPIExposesNoSecretAndTestsDelivery(t *testing.T) {
	var posts atomic.Int32
	ts, db, st, logs := notifyServer(t, true, http.StatusNoContent, &posts)

	code, body := call(t, ts, "POST", "/api/notifications/test", "", "{}")
	if code != 200 || body["ok"] != true || body["http_status"] != float64(204) || posts.Load() != 1 {
		t.Fatalf("%d %v posts=%d", code, body, posts.Load())
	}

	resp, err := http.Get(ts.URL + "/api/notifications")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `"configured":true`) || !strings.Contains(string(raw), `"suffix":"wxyz"`) ||
		!strings.Contains(string(raw), `"recent_deliveries":[{"event":"test","ok":true,"http_status":204`) {
		t.Fatalf("%s", raw)
	}

	// Toggle round trip, validation and unknown events.
	if code, _ := call(t, ts, "PUT", "/api/notifications/events/version_approved", "", `{"enabled":true}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := call(t, ts, "PUT", "/api/notifications/events/version_approved", "", `{}`); code != 400 {
		t.Fatal(code)
	}
	if code, _ := call(t, ts, "PUT", "/api/notifications/events/bogus", "", `{"enabled":true}`); code != 404 {
		t.Fatal(code)
	}

	// Approving and exporting queue messages (idempotently keyed), and never touch Discord inline.
	v := seedVideo(t, db, st, []byte("fake-mp4-notify"), "qa_passed")
	id := strconv.FormatInt(v.ID, 10)
	if code, b := call(t, ts, "POST", "/api/videos/"+id+"/decision", "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`); code != 201 {
		t.Fatalf("%d %v", code, b)
	}
	if code, b := call(t, ts, "POST", "/api/videos/"+id+"/export", "", "{}"); code != 201 {
		t.Fatalf("%d %v", code, b)
	}
	var queued int
	db.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE event = 'version_approved' AND status = 'pending'`).Scan(&queued)
	if queued != 2 || posts.Load() != 1 {
		t.Fatalf("queued=%d posts=%d", queued, posts.Load())
	}

	for _, s := range []string{hookURL, "APITOKENsecret", "987654321"} {
		if strings.Contains(string(raw), s) || strings.Contains(logs.String(), s) {
			t.Fatalf("leaked %q", s)
		}
	}
}

func TestNotificationDeliveryFailureDoesNotFailTheDecision(t *testing.T) {
	var posts atomic.Int32
	ts, db, st, _ := notifyServer(t, true, http.StatusInternalServerError, &posts)
	call(t, ts, "PUT", "/api/notifications/events/version_approved", "", `{"enabled":true}`)
	code, body := call(t, ts, "POST", "/api/notifications/test", "", "{}")
	if code != 200 || body["ok"] != false || body["http_status"] != float64(500) {
		t.Fatalf("the real HTTP result must be shown: %d %v", code, body)
	}
	v := seedVideo(t, db, st, []byte("fake-mp4-notify-fail"), "qa_passed")
	if code, b := call(t, ts, "POST", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/decision", "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`); code != 201 {
		t.Fatalf("%d %v", code, b)
	}
}
