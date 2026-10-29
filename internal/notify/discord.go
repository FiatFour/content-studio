// Package notify sends outbound-only Discord webhook notifications (D12, notifications.md).
// Notifications never carry actions. The webhook URL is a secret: it is never logged and never
// appears in a returned error or result.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Message is one rendered notification. Text only.
type Message struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
}

// Result is the real outcome of one delivery attempt. Error is a sanitized class, never raw text.
type Result struct {
	OK         bool
	HTTPStatus int
	Error      string
	RetryAfter time.Duration
}

// Notifier delivers a message. It is the only seam tests replace.
type Notifier interface {
	Send(ctx context.Context, m Message) Result
}

var allowedHosts = map[string]bool{"discord.com": true, "discordapp.com": true}

var errBadURL = errors.New("not a valid Discord webhook URL")

// ValidateWebhookURL is the SSRF guard: https only, a Discord host allowlist, no credentials,
// no port, no query, and the webhook path. The returned error never contains the input.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errBadURL
	}
	if !allowedHosts[strings.ToLower(u.Host)] { // Host includes any port, so a port fails the allowlist
		return errBadURL
	}
	rest, ok := strings.CutPrefix(u.Path, "/api/webhooks/")
	if !ok || strings.Count(rest, "/") != 1 || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") {
		return errBadURL
	}
	return nil
}

// MaskSuffix returns the last 4 characters of the URL, the most the UI may show.
func MaskSuffix(raw string) string {
	if len(raw) < 4 {
		return ""
	}
	return raw[len(raw)-4:]
}

type Discord struct {
	url    string
	client *http.Client
}

// NewDiscord validates the URL. client may be nil; tests pass one whose transport reaches a fake
// receiver, so validation stays strict even under test. Redirects are never followed.
func NewDiscord(webhookURL string, client *http.Client) (*Discord, error) {
	if err := ValidateWebhookURL(webhookURL); err != nil {
		return nil, err
	}
	c := http.Client{Timeout: 10 * time.Second}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Discord{url: webhookURL, client: &c}, nil
}

// Suffix is the last 4 characters of the webhook URL, for the "configured (...xxxx)" status.
func (d *Discord) Suffix() string { return MaskSuffix(d.url) }

const maxResponse = 64 << 10

func (d *Discord) Send(ctx context.Context, m Message) Result {
	body, err := json.Marshal(map[string]any{
		"username":         "AI Content Studio",
		"allowed_mentions": map[string]any{"parse": []string{}},
		"embeds":           []map[string]any{{"title": m.Title, "description": m.Description, "color": m.Color}},
	})
	if err != nil {
		return Result{Error: "could not build message"}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return Result{Error: "could not build request"}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		// err is a *url.Error that embeds the webhook URL. Classify it and drop the text.
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return Result{Error: "timeout"}
		}
		return Result{Error: "network error"}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse)) // only a 429 needs the body
	r := Result{HTTPStatus: resp.StatusCode}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		r.OK = true
	case resp.StatusCode == http.StatusTooManyRequests:
		r.Error = "rate limited"
		r.RetryAfter = retryAfter(resp, data)
	default:
		r.Error = "discord returned HTTP " + strconv.Itoa(resp.StatusCode)
	}
	return r
}

// retryAfter reads Discord's retry_after (seconds, may be fractional) or the Retry-After header.
func retryAfter(resp *http.Response, body []byte) time.Duration {
	var v struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &v) == nil && v.RetryAfter > 0 {
		return time.Duration(v.RetryAfter * float64(time.Second))
	}
	if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	return 0
}
