package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/auth"
)

// Limits are the request-size and rate limits. A zero field means the default. They live in memory only:
// a restart resets every rate counter.
type Limits struct {
	MaxBody       int64 // bytes, any state-changing /api request; default 1 MiB
	AnonPerMinute int   // /api requests per client IP without a session cookie; default 120
	AuthPerMinute int   // /api requests per session cookie; default 1200
	// StreamWrite is how long one artifact download (video) may take to write; the server-wide
	// WriteTimeout would cut long streams. Default 15 minutes.
	StreamWrite time.Duration
}

const (
	DefaultMaxBody       = 1 << 20
	DefaultAnonPerMinute = 120
	DefaultAuthPerMinute = 1200
	DefaultStreamWrite   = 15 * time.Minute
	smallBody            = 16 << 10 // JSON bodies that carry a few fields
	authBody             = 64 << 10 // WebAuthn ceremony payloads (matches readBody)
)

func (l Limits) withDefaults() Limits {
	if l.MaxBody <= 0 {
		l.MaxBody = DefaultMaxBody
	}
	if l.AnonPerMinute <= 0 {
		l.AnonPerMinute = DefaultAnonPerMinute
	}
	if l.AuthPerMinute <= 0 {
		l.AuthPerMinute = DefaultAuthPerMinute
	}
	if l.StreamWrite <= 0 {
		l.StreamWrite = DefaultStreamWrite
	}
	return l
}

// bodyLimit is the per-endpoint override: auth ceremonies and the tiny settings/job/review calls get
// small caps; everything else (episode ideas, canon text) gets the global one. No endpoint takes uploads.
func (l Limits) bodyLimit(path string) int64 {
	small := func(n int64) int64 { return min(n, l.MaxBody) }
	switch {
	case strings.HasPrefix(path, "/api/auth/"):
		return small(authBody)
	case path == "/api/settings", strings.HasPrefix(path, "/api/resources/"), strings.HasPrefix(path, "/api/notifications/"),
		strings.HasPrefix(path, "/api/runs/"), strings.HasPrefix(path, "/api/videos/"):
		return small(smallBody)
	}
	return l.MaxBody
}

// csp is tuned to the Vite build served from this origin: bundled script and CSS, React inline style
// attributes, Google Fonts (web/index.html), same-origin images and video, fetch to /api only.
// The review player and the images use same-origin URLs, so blob: is not allowed.
const csp = "default-src 'none'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; style-src-attr 'unsafe-inline'; " +
	"font-src https://fonts.gstatic.com; img-src 'self' data:; media-src 'self'; connect-src 'self'; manifest-src 'self'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"

// permissions allows only what the UI uses: WebAuthn (passkeys) and fullscreen for the player.
const permissions = "publickey-credentials-get=(self), publickey-credentials-create=(self), fullscreen=(self), " +
	"camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), midi=(), " +
	"display-capture=(), accelerometer=(), gyroscope=(), magnetometer=()"

// hsts: HSTS is sent only when the request is TLS itself, or its Host is the host of a configured
// https origin (a TLS-terminating proxy in front). Plain http://localhost never gets it.
func (s *Server) hsts(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.Auth == nil {
		return false
	}
	for _, o := range s.Auth.Origins() {
		if u, err := url.Parse(o); err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, r.Host) {
			return true
		}
	}
	return false
}

// securityHeaders runs before routing (Pre) so 404s, 401s, 413s, 429s and SPA files all carry the headers.
func (s *Server) securityHeaders(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		h := c.Response().Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", permissions)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if s.hsts(c.Request()) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if isAPI(c.Request().URL.Path) {
			h.Set("Cache-Control", "no-store") // artifact content overrides this with its own immutable cache
		}
		return next(c)
	}
}

// requestLimits enforces, for state-changing /api requests: declared and actual body size (413) and the
// JSON content type (415).
func (s *Server) requestLimits(next echo.HandlerFunc) echo.HandlerFunc {
	lim := s.Limits.withDefaults()
	return func(c echo.Context) error {
		r := c.Request()
		if !isAPI(r.URL.Path) || !stateChanging(r.Method) {
			return next(c)
		}
		limit := lim.bodyLimit(r.URL.Path)
		if r.ContentLength > limit {
			return fail(http.StatusRequestEntityTooLarge, "request body too large")
		}
		if r.ContentLength != 0 { // a body may exist (-1 means chunked)
			// Forms and text/plain are the "simple" types a cross-site page can send without a preflight.
			// Every body this API takes is JSON, so anything else is refused.
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" {
				return fail(http.StatusUnsupportedMediaType, "content type must be application/json")
			}
			// Read it all now so an oversized chunked body is a clean 413, not a handler 400.
			b, err := io.ReadAll(http.MaxBytesReader(c.Response(), r.Body, limit))
			if err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					return fail(http.StatusRequestEntityTooLarge, "request body too large")
				}
				return fail(http.StatusBadRequest, "invalid request body")
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
		}
		return next(c)
	}
}

type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter is a token bucket per key. Buckets idle for a minute are pruned; if the map is still full
// (a flood of forged cookies) new keys share one overflow bucket, so memory stays bounded.
type rateLimiter struct {
	mu      sync.Mutex
	b       map[string]*bucket
	now     func() time.Time
	maxKeys int
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{b: map[string]*bucket{}, now: time.Now, maxKeys: 10000}
}

// allow takes a token from key's bucket (perMinute refill, burst perMinute/4 but at least 10). When
// refused it returns the wait until a token is available.
func (l *rateLimiter) allow(key string, perMinute int) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	burst := float64(max(perMinute/4, 10))
	rate := float64(perMinute) / 60 // tokens per second
	bk := l.b[key]
	if bk == nil {
		if len(l.b) >= l.maxKeys {
			for k, v := range l.b {
				if now.Sub(v.last) > time.Minute {
					delete(l.b, k)
				}
			}
		}
		if len(l.b) >= l.maxKeys {
			key = "overflow"
			bk = l.b[key]
		}
		if bk == nil {
			bk = &bucket{tokens: burst, last: now}
			l.b[key] = bk
		}
	}
	bk.tokens = min(burst, bk.tokens+now.Sub(bk.last).Seconds()*rate)
	bk.last = now
	if bk.tokens >= 1 {
		bk.tokens--
		return true, 0
	}
	return false, time.Duration((1 - bk.tokens) / rate * float64(time.Second))
}

// rateLimit applies to /api except GET /api/health. Requests with a session cookie are counted per
// cookie (a hash of its value), everything else per client IP, so on one machine a flood of anonymous
// requests cannot use up the signed-in owner's allowance. The cookie is not validated here; forged
// cookies each get their own small bucket and cost one database lookup per request, bounded by this
// limit. The login/enroll failure limiter (internal/auth) is separate and counts only failed ceremonies;
// either can answer 429, the stricter one wins.
func (s *Server) rateLimit(next echo.HandlerFunc) echo.HandlerFunc {
	lim := s.Limits.withDefaults()
	rl := s.rl // Echo builds the middleware chain per request, so the limiter must live on the Server
	return func(c echo.Context) error {
		r := c.Request()
		if !isAPI(r.URL.Path) || (r.Method == http.MethodGet && r.URL.Path == "/api/health") {
			return next(c)
		}
		key, per := "ip:"+clientIP(c), lim.AnonPerMinute
		if ck, err := r.Cookie(auth.CookieName); err == nil && ck.Value != "" {
			sum := sha256.Sum256([]byte(ck.Value))
			key, per = "s:"+hex.EncodeToString(sum[:8]), lim.AuthPerMinute
		}
		if ok, wait := rl.allow(key, per); !ok {
			c.Response().Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
			return fail(http.StatusTooManyRequests, "too many requests, slow down")
		}
		return next(c)
	}
}

// streamDeadline lifts the server-wide WriteTimeout for artifact downloads only (video playback with
// range requests), replacing it with Limits.StreamWrite.
func (s *Server) streamDeadline(next echo.HandlerFunc) echo.HandlerFunc {
	d := s.Limits.withDefaults().StreamWrite
	return func(c echo.Context) error {
		p := c.Request().URL.Path
		if c.Request().Method == http.MethodGet && strings.HasPrefix(p, "/api/artifacts/") && strings.HasSuffix(p, "/content") {
			_ = http.NewResponseController(c.Response()).SetWriteDeadline(time.Now().Add(d)) // a writer without deadlines keeps the default
		}
		return next(c)
	}
}
