// Package auth is passkey (WebAuthn) authentication for the single owner (D9). The WebAuthn
// cryptography is github.com/go-webauthn/webauthn; nothing here verifies signatures itself.
//
// Secrets: session tokens and enrollment codes exist in plaintext only in the response/terminal
// that hands them out. The database holds their SHA-256. Neither is ever logged or audited.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CookieName is the session cookie. It carries the raw token and nothing else.
const CookieName = "studio_session"

const (
	ceremonyTTL = 5 * time.Minute
	maxCeremony = 64
	// EnrollCodeTTL is how long a code from `server enroll` stays valid.
	EnrollCodeTTL = 10 * time.Minute
	// advisory lock key serializing credential-set changes (first enrollment, removal).
	credLockKey = 7262002
)

var (
	// ErrUnauthenticated is the only reason the API gives for a refused session.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrAuthFailed is any failed login or enrollment ceremony; callers learn nothing more.
	ErrAuthFailed = errors.New("authentication failed")
	// ErrAlreadyEnrolled: a passkey exists, so a code cannot create another one.
	ErrAlreadyEnrolled = errors.New("a passkey is already enrolled")
	// ErrLastCredential: the only passkey cannot be removed.
	ErrLastCredential = errors.New("cannot remove the last passkey")
	ErrNotFound       = errors.New("not found")
	ErrRateLimited    = errors.New("too many attempts")
)

// Config configures the service. Zero durations get the defaults noted below.
type Config struct {
	RPID        string   // e.g. "localhost"
	Origins     []string // allowed browser origins, e.g. "http://localhost:8080"
	DisplayName string
	IdleTTL     time.Duration // default 12h without a request
	MaxTTL      time.Duration // default 7d since login, regardless of activity
	Now         func() time.Time
}

type Service struct {
	DB *pgxpool.Pool
	WA *webauthn.WebAuthn

	cfg Config

	mu         sync.Mutex
	ceremonies map[string]*ceremony
	limiter    limiter
}

// New validates the WebAuthn configuration.
func New(db *pgxpool.Pool, cfg Config) (*Service, error) {
	if cfg.DisplayName == "" {
		cfg.DisplayName = "AI Content Studio"
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 12 * time.Hour
	}
	if cfg.MaxTTL <= 0 {
		cfg.MaxTTL = 7 * 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	wa, err := webauthn.New(&webauthn.Config{RPID: cfg.RPID, RPDisplayName: cfg.DisplayName, RPOrigins: cfg.Origins})
	if err != nil {
		return nil, fmt.Errorf("webauthn config: %w", err)
	}
	return &Service{DB: db, WA: wa, cfg: cfg, ceremonies: map[string]*ceremony{}, limiter: limiter{max: 10, window: 10 * time.Minute}}, nil
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// Origins are the configured browser origins, used by the API's CSRF origin check.
func (s *Service) Origins() []string { return s.cfg.Origins }

// MaxAge is the absolute session lifetime (cookie Max-Age).
func (s *Service) MaxAge() time.Duration { return s.cfg.MaxTTL }

// Session is an authenticated session.
type Session struct {
	ID           int64
	CredentialID *int64
	Kind         string
}

// Actor is the identity recorded in the audit log for actions taken in this session.
func (s Session) Actor() string {
	if s.CredentialID == nil {
		return "test-session"
	}
	return fmt.Sprintf("owner:passkey-%d", *s.CredentialID)
}

func hash(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func newToken() (token string, h []byte, err error) {
	b := make([]byte, 32) // 256 bits
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hash(token), nil
}

// CreateSession starts a session and returns the raw token, which is the only copy. credentialID is
// nil only for kind "test" (see the migration).
func (s *Service) CreateSession(ctx context.Context, credentialID *int64, kind string) (string, error) {
	return createSession(ctx, s.DB, s.now(), s.cfg.MaxTTL, credentialID, kind)
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func createSession(ctx context.Context, q queryRower, now time.Time, ttl time.Duration, credentialID *int64, kind string) (string, error) {
	token, h, err := newToken()
	if err != nil {
		return "", err
	}
	var id int64
	err = q.QueryRow(ctx, `INSERT INTO auth_sessions (token_hash, credential_id, kind, created_at, expires_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $4) RETURNING id`, h, credentialID, kind, now, now.Add(ttl)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// Authenticate resolves a raw token. Missing, unknown, expired (idle or absolute) and revoked tokens
// are indistinguishable to the caller. A valid session's last_seen advances.
func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	if token == "" || len(token) > 128 {
		return nil, ErrUnauthenticated
	}
	now := s.now()
	var sess Session
	err := s.DB.QueryRow(ctx, `UPDATE auth_sessions SET last_seen_at = $2
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $2 AND last_seen_at > $3
		RETURNING id, credential_id, kind`, hash(token), now, now.Add(-s.cfg.IdleTTL)).Scan(&sess.ID, &sess.CredentialID, &sess.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	return &sess, nil
}

// ceremony is the server-side state of one WebAuthn registration or login in progress.
type ceremony struct {
	data      webauthn.SessionData
	register  bool
	codeID    int64 // enrollment code this registration consumes; 0 when an existing session adds a passkey
	sessionID int64 // session that began an add-passkey registration
	expires   time.Time
}

func (s *Service) putCeremony(c *ceremony) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	now := s.now()
	c.expires = now.Add(ceremonyTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.ceremonies {
		if now.After(v.expires) {
			delete(s.ceremonies, k)
		}
	}
	if len(s.ceremonies) >= maxCeremony {
		return "", ErrRateLimited
	}
	s.ceremonies[id] = c
	return id, nil
}

// takeCeremony returns the ceremony once; a second call with the same id finds nothing.
func (s *Service) takeCeremony(id string, register bool) (*ceremony, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ceremonies[id]
	if !ok {
		return nil, false
	}
	delete(s.ceremonies, id)
	if s.now().After(c.expires) || c.register != register {
		return nil, false
	}
	return c, true
}

// NewEnrollCode returns a one-time code, valid for EnrollCodeTTL, that lets the first passkey be
// registered. The caller must print it to the terminal only. Any earlier unused code is dropped.
func (s *Service) NewEnrollCode(ctx context.Context) (string, time.Time, error) {
	raw := make([]byte, 13) // 104 bits
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw) // 21 chars
	enc = enc[:20]
	code := enc[0:5] + "-" + enc[5:10] + "-" + enc[10:15] + "-" + enc[15:20]
	exp := s.now().Add(EnrollCodeTTL)
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(credLockKey)); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_credentials`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadyEnrolled
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_enroll_codes WHERE used_at IS NULL`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO auth_enroll_codes (code_hash, created_at, expires_at) VALUES ($1, $2, $3)`,
			hash(normalizeCode(code)), s.now(), exp); err != nil {
			return err
		}
		return recordSystem(ctx, tx, "cli", "auth.enroll_code.create", nil)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return code, exp, nil
}

func normalizeCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

// Enrolled reports whether any passkey exists.
func (s *Service) Enrolled(ctx context.Context) (bool, error) {
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM auth_credentials`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
