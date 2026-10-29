package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/notify"
	"github.com/fiat/content-studio/internal/store/storetest"
)

const secretURL = "https://discord.com/api/webhooks/123456789/SECRETtokenVALUE-abcd"

// toFake sends every request to the fake receiver while the client still sees the real,
// validated Discord URL. No test ever reaches Discord.
type toFake struct {
	target *url.URL
	fail   error
}

func (t toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.fail != nil {
		return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: t.fail}
	}
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

type receiver struct {
	mu      sync.Mutex
	bodies  []string
	respond func(n int, w http.ResponseWriter)
}

func (rc *receiver) count() int { rc.mu.Lock(); defer rc.mu.Unlock(); return len(rc.bodies) }

func fake(t *testing.T, respond func(n int, w http.ResponseWriter)) (*receiver, *http.Client) {
	rc := &receiver{respond: respond}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.bodies = append(rc.bodies, string(b))
		n := len(rc.bodies)
		rc.mu.Unlock()
		rc.respond(n, w)
	}))
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	return rc, &http.Client{Transport: toFake{target: u}}
}

func ok(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func TestValidateWebhookURL(t *testing.T) {
	good := []string{secretURL, "https://discordapp.com/api/webhooks/1/abc_DEF-9"}
	for _, u := range good {
		if err := notify.ValidateWebhookURL(u); err != nil {
			t.Errorf("%s rejected: %v", u, err)
		}
	}
	bad := []string{
		"", "http://discord.com/api/webhooks/1/abc", "https://evil.example/api/webhooks/1/abc",
		"https://discord.com.evil.example/api/webhooks/1/abc", "https://127.0.0.1/api/webhooks/1/abc",
		"https://localhost/api/webhooks/1/abc", "https://discord.com:8443/api/webhooks/1/abc",
		"https://user:pw@discord.com/api/webhooks/1/abc", "https://discord.com/api/webhooks/1",
		"https://discord.com/other/1/abc", "https://discord.com/api/webhooks/1/abc?x=1",
		"https://discord.com/api/webhooks/1/abc/extra", "https://notdiscord.com/api/webhooks/1/abc", "file:///etc/passwd",
	}
	for _, u := range bad {
		err := notify.ValidateWebhookURL(u)
		if err == nil {
			t.Errorf("%q accepted", u)
		} else if u != "" && strings.Contains(err.Error(), u) {
			t.Errorf("error echoes the URL: %v", err)
		}
		if _, err := notify.NewDiscord(u, nil); err == nil {
			t.Errorf("NewDiscord accepted %q", u)
		}
	}
}

func TestSendResults(t *testing.T) {
	rc, client := fake(t, func(n int, w http.ResponseWriter) {
		switch n {
		case 1:
			w.WriteHeader(http.StatusNoContent)
		case 2:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"retry_after": 1.5}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	d, err := notify.NewDiscord(secretURL, client)
	if err != nil {
		t.Fatal(err)
	}
	if d.Suffix() != "abcd" {
		t.Fatalf("suffix %q", d.Suffix())
	}
	msg := notify.BuildMessage(notify.Test, notify.Info{})
	if r := d.Send(context.Background(), msg); !r.OK || r.HTTPStatus != 204 {
		t.Fatalf("%+v", r)
	}
	if r := d.Send(context.Background(), msg); r.OK || r.HTTPStatus != 429 || r.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("%+v", r)
	}
	if r := d.Send(context.Background(), msg); r.OK || r.HTTPStatus != 500 || r.Error == "" {
		t.Fatalf("%+v", r)
	}
	var body struct {
		Mentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal([]byte(rc.bodies[0]), &body); err != nil || body.Mentions.Parse == nil || len(body.Mentions.Parse) != 0 {
		t.Fatalf("mentions not disabled: %s", rc.bodies[0])
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var hits int
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer evil.Close()
	_, client := fake(t, func(_ int, w http.ResponseWriter) {
		w.Header().Set("Location", evil.URL)
		w.WriteHeader(http.StatusFound)
	})
	d, _ := notify.NewDiscord(secretURL, client)
	r := d.Send(context.Background(), notify.BuildMessage(notify.Test, notify.Info{}))
	if r.OK || hits != 0 {
		t.Fatalf("redirect followed: %+v hits=%d", r, hits)
	}
}

func TestSendErrorsNeverContainTheURL(t *testing.T) {
	for _, cause := range []error{errors.New("dial tcp: connection refused to " + secretURL), context.DeadlineExceeded} {
		d, _ := notify.NewDiscord(secretURL, &http.Client{Transport: toFake{fail: cause}})
		r := d.Send(context.Background(), notify.BuildMessage(notify.Test, notify.Info{}))
		if r.OK || r.Error == "" || strings.Contains(r.Error, "SECRET") || strings.Contains(r.Error, "discord.com") {
			t.Fatalf("%+v", r)
		}
	}
}

func newService(t *testing.T, n notify.Notifier, logs io.Writer) *notify.Service {
	return &notify.Service{DB: storetest.New(t), Notifier: n, Suffix: "abcd",
		Log:     slog.New(slog.NewJSONHandler(logs, nil)),
		Backoff: func(int) time.Duration { return 0 }, MaxAttempts: 3}
}

func episode(t *testing.T, s *notify.Service) int64 {
	_, id := storetest.Episode(t, s.DB)
	return id
}

func makeDue(t *testing.T, s *notify.Service) {
	if _, err := s.DB.Exec(context.Background(), `UPDATE notifications SET next_attempt_at = now() WHERE status = 'pending'`); err != nil {
		t.Fatal(err)
	}
}

func statusOf(t *testing.T, s *notify.Service, eventID string) (status string, attempts int) {
	if err := s.DB.QueryRow(context.Background(), `SELECT status, attempts FROM notifications WHERE event_id = $1`, eventID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	return
}

func TestNoDeliveryWhenUnset(t *testing.T) {
	s := newService(t, nil, io.Discard)
	ep := episode(t, s)
	if err := notify.Enqueue(context.Background(), s.DB, notify.EpisodeReady, "e1", ep, 1, "QA passed", ""); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.RunOne(context.Background()); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if st, _ := statusOf(t, s, "e1"); st != "skipped" {
		t.Fatalf("status %s", st)
	}
	if s.Configured() {
		t.Fatal("reports configured")
	}
	if _, err := s.SendTest(context.Background()); !errors.Is(err, notify.ErrNotConfigured) {
		t.Fatalf("%v", err)
	}
}

func TestEnqueueIsIdempotentAndHonoursToggles(t *testing.T) {
	rc, client := fake(t, ok)
	d, _ := notify.NewDiscord(secretURL, client)
	s := newService(t, d, io.Discard)
	ep := episode(t, s)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := notify.Enqueue(ctx, s.DB, notify.EpisodeReady, "same", ep, 1, "QA passed", ""); err != nil {
			t.Fatal(err)
		}
	}
	for {
		ran, err := s.RunOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	if rc.count() != 1 {
		t.Fatalf("%d posts for one event_id", rc.count())
	}
	// Default-off event is not queued; turning it on queues it; turning episode_ready off stops it.
	if err := notify.Enqueue(ctx, s.DB, notify.VersionApproved, "a1", ep, 1, "approved", ""); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id = 'a1'`).Scan(&n)
	if n != 0 {
		t.Fatal("default-off event was queued")
	}
	if err := s.SetEnabled(ctx, notify.VersionApproved, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(ctx, notify.EpisodeReady, false); err != nil {
		t.Fatal(err)
	}
	notify.Enqueue(ctx, s.DB, notify.VersionApproved, "a1", ep, 1, "approved", "")
	notify.Enqueue(ctx, s.DB, notify.EpisodeReady, "e2", ep, 2, "QA passed", "")
	s.DB.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id IN ('a1','e2')`).Scan(&n)
	if n != 1 {
		t.Fatalf("toggles not honoured, %d queued", n)
	}
	if err := s.SetEnabled(ctx, "nonsense", true); !errors.Is(err, notify.ErrUnknownEvent) {
		t.Fatal(err)
	}
	toggles, err := s.Toggles(ctx)
	if err != nil || len(toggles) != len(notify.Events) {
		t.Fatal(toggles, err)
	}
	if err := notify.Enqueue(ctx, s.DB, notify.JobFailed, "x", ep, 1, "", ""); err == nil {
		t.Fatal("an unwired event must not be enqueueable")
	}
}

func TestRateLimitRetryIsNotADuplicatePost(t *testing.T) {
	rc, client := fake(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"retry_after": 120}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	d, _ := notify.NewDiscord(secretURL, client)
	s := newService(t, d, io.Discard)
	ep := episode(t, s)
	ctx := context.Background()
	notify.Enqueue(ctx, s.DB, notify.EpisodeReady, "e1", ep, 1, "QA passed", "")
	s.RunOne(ctx)
	st, att := statusOf(t, s, "e1")
	if st != "pending" || att != 1 {
		t.Fatalf("after 429: %s attempts=%d", st, att)
	}
	// Not due yet: honouring retry_after (120 s), so nothing is sent.
	if ran, _ := s.RunOne(ctx); ran || rc.count() != 1 {
		t.Fatalf("retried before retry_after: ran=%v posts=%d", ran, rc.count())
	}
	var wait float64
	s.DB.QueryRow(ctx, `SELECT extract(epoch FROM next_attempt_at - now()) FROM notifications WHERE event_id = 'e1'`).Scan(&wait)
	if wait < 100 || wait > 121 {
		t.Fatalf("retry wait %.0fs does not follow retry_after", wait)
	}
	makeDue(t, s)
	s.RunOne(ctx)
	if st, _ := statusOf(t, s, "e1"); st != "sent" {
		t.Fatalf("status %s", st)
	}
	makeDue(t, s)
	if ran, _ := s.RunOne(ctx); ran || rc.count() != 2 {
		t.Fatalf("duplicate post: posts=%d", rc.count())
	}
	recent, _ := s.Recent(ctx, 10)
	if len(recent) != 2 || recent[0].OK != true || *recent[1].HTTPStatus != 429 {
		t.Fatalf("%+v", recent)
	}
}

func TestRetriesAreBoundedAndFailureIsVisible(t *testing.T) {
	rc, client := fake(t, func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	d, _ := notify.NewDiscord(secretURL, client)
	s := newService(t, d, io.Discard)
	ep := episode(t, s)
	notify.Enqueue(context.Background(), s.DB, notify.EpisodeReady, "e1", ep, 1, "QA passed", "")
	for i := 0; i < 10; i++ {
		makeDue(t, s)
		s.RunOne(context.Background())
	}
	if st, att := statusOf(t, s, "e1"); st != "failed" || att != 3 || rc.count() != 3 {
		t.Fatalf("%s attempts=%d posts=%d", st, att, rc.count())
	}
}

// A dead Discord must not stall callers: Send honours its context and returns a timeout class.
func TestHungDiscordTimesOut(t *testing.T) {
	block := make(chan struct{})
	_, client := fake(t, func(int, http.ResponseWriter) { <-block })
	defer close(block)
	d, _ := notify.NewDiscord(secretURL, client)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := d.Send(ctx, notify.BuildMessage(notify.Test, notify.Info{}))
	if r.OK || r.Error != "timeout" || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v after %v", r, time.Since(start))
	}
}

func TestWebhookURLNeverLeaks(t *testing.T) {
	var logs bytes.Buffer
	var results []notify.Result
	// Transport errors embed the full URL, the worst case for a leak.
	d, _ := notify.NewDiscord(secretURL, &http.Client{Transport: toFake{fail: errors.New("boom " + secretURL)}})
	s := newService(t, d, &logs)
	ep := episode(t, s)
	ctx := context.Background()
	notify.Enqueue(ctx, s.DB, notify.EpisodeReady, "e1", ep, 1, "QA passed", "see "+secretURL+" at /home/fiat/x/y.log")
	for i := 0; i < 4; i++ {
		makeDue(t, s)
		if _, err := s.RunOne(ctx); err != nil {
			t.Fatalf("%v", err)
		}
	}
	r, err := s.SendTest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	results = append(results, r)

	var everything strings.Builder
	everything.WriteString(logs.String())
	for _, r := range results {
		everything.WriteString(r.Error)
	}
	for _, q := range []string{`SELECT payload::text FROM notifications`, `SELECT error FROM notification_deliveries`} {
		rows, err := s.DB.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var v string
			rows.Scan(&v)
			everything.WriteString(v)
		}
		rows.Close()
	}
	all := everything.String()
	for _, secret := range []string{secretURL, "SECRETtokenVALUE", "123456789/SECRET", "/api/webhooks"} {
		if strings.Contains(all, secret) {
			t.Fatalf("leaked %q in logs/errors/db:\n%s", secret, all)
		}
	}
	if logs.Len() == 0 {
		t.Fatal("expected some log output, so the check above means something")
	}
}

func TestMessageBodiesAreSafe(t *testing.T) {
	nasty := "panic: boom\ngoroutine 1 [running]:\n/home/fiat/project/content-studio/internal/x.go:42 +0x1\nsee " + secretURL + " C:/Users/x/y.txt"
	for _, ev := range notify.Events {
		m := notify.BuildMessage(ev.Event, notify.Info{EpisodeNumber: 7, Title: "ยายกับมือถือ", Version: 2, State: "approved", Reason: nasty})
		text := m.Title + "\n" + m.Description
		for _, bad := range []string{"/home/", "goroutine", ".go:", "https://", "SECRET", "C:/", "/api/"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s message contains %q: %s", ev.Event, bad, text)
			}
		}
		if strings.Count(m.Description, "\n") > 3 || len([]rune(m.Description)) > 400 {
			t.Errorf("%s message not short: %q", ev.Event, m.Description)
		}
	}
	m := notify.BuildMessage(notify.EpisodeReady, notify.Info{EpisodeNumber: 7, Title: "ยายกับมือถือ", Version: 2, State: "QA passed"})
	if !strings.Contains(m.Description, "Episode 7") || !strings.Contains(m.Description, "ยายกับมือถือ") || !strings.Contains(m.Description, "v2") {
		t.Fatalf("%q", m.Description)
	}
	if !strings.Contains(notify.BuildMessage(notify.Test, notify.Info{}).Title, "TEST") {
		t.Fatal("test message not labelled")
	}
}
