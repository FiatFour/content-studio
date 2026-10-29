package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/audit"
)

// ErrInvalidLabel is a passkey label that is empty after trimming or longer than 80 characters.
var ErrInvalidLabel = errors.New("label must be 1 to 80 characters")

// owner is the single WebAuthn user.
type owner struct {
	handle []byte
	creds  []webauthn.Credential
}

func (o *owner) WebAuthnID() []byte                         { return o.handle }
func (o *owner) WebAuthnName() string                       { return "owner" }
func (o *owner) WebAuthnDisplayName() string                { return "Studio owner" }
func (o *owner) WebAuthnCredentials() []webauthn.Credential { return o.creds }

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadOwner returns the owner with all credentials. With create it first makes the owner row.
func loadOwner(ctx context.Context, q querier, create bool) (*owner, error) {
	if create {
		h := make([]byte, 32)
		if _, err := rand.Read(h); err != nil {
			return nil, err
		}
		var dummy int
		// QueryRow on INSERT ... RETURNING keeps this on the querier interface; no row means it existed.
		err := q.QueryRow(ctx, `INSERT INTO auth_owner (id, user_handle) VALUES (1, $1) ON CONFLICT (id) DO NOTHING RETURNING 1`, h).Scan(&dummy)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	o := &owner{}
	if err := q.QueryRow(ctx, `SELECT user_handle FROM auth_owner WHERE id = 1`).Scan(&o.handle); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT credential_id, public_key, sign_count, transports, flags, attestation_type, aaguid, clone_warning
		FROM auth_credentials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	o.creds, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (webauthn.Credential, error) {
		var c webauthn.Credential
		var transports []string
		var flags int16
		var count int64
		err := r.Scan(&c.ID, &c.PublicKey, &count, &transports, &flags, &c.AttestationType, &c.Authenticator.AAGUID, &c.Authenticator.CloneWarning)
		c.Authenticator.SignCount = uint32(count)
		c.Flags = webauthn.NewCredentialFlags(protocol.AuthenticatorFlags(byte(flags)))
		for _, t := range transports {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
		return c, err
	})
	return o, err
}

func recordSystem(ctx context.Context, tx pgx.Tx, actor, action string, detail map[string]any) error {
	return audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: action, TargetType: "auth", Detail: detail})
}

// auditFailure records a failed login or enrollment. reason is a fixed word, never input from the request.
func (s *Service) auditFailure(ctx context.Context, action, reason string) {
	_ = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		return recordSystem(ctx, tx, "anonymous", action, map[string]any{"reason": reason})
	})
}

func cleanLabel(l string) (string, error) {
	l = strings.TrimSpace(l)
	if l == "" {
		return "Passkey", nil
	}
	if utf8.RuneCountInString(l) > 80 {
		return "", ErrInvalidLabel
	}
	return l, nil
}

// registrationOptions require a discoverable credential with user verification: the passkey is the
// only factor, so it must prove the person, not just the device.
func registrationOptions(o *owner) []webauthn.RegistrationOption {
	yes := true
	return []webauthn.RegistrationOption{
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: &yes, ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(webauthn.Credentials(o.creds).CredentialDescriptors()),
	}
}

// BeginRegistration starts a passkey registration. With a session it adds another passkey. Without
// one it needs an unused, unexpired enrollment code, and is refused if any passkey already exists.
func (s *Service) BeginRegistration(ctx context.Context, code string, sess *Session, ip string) (string, *protocol.CredentialCreation, error) {
	c := &ceremony{register: true}
	if sess != nil {
		c.sessionID = sess.ID
	} else {
		now := s.now()
		if !s.limiter.allow(ip, now) {
			return "", nil, ErrRateLimited
		}
		enrolled, err := s.Enrolled(ctx)
		if err != nil {
			return "", nil, err
		}
		if enrolled {
			s.auditFailure(ctx, "auth.enroll.failure", "already_enrolled")
			return "", nil, ErrAlreadyEnrolled
		}
		err = s.DB.QueryRow(ctx, `SELECT id FROM auth_enroll_codes WHERE code_hash = $1 AND used_at IS NULL AND expires_at > $2`,
			hash(normalizeCode(code)), now).Scan(&c.codeID)
		if errors.Is(err, pgx.ErrNoRows) {
			s.limiter.fail(ip, now)
			s.auditFailure(ctx, "auth.enroll.failure", "bad_code")
			return "", nil, ErrAuthFailed
		}
		if err != nil {
			return "", nil, err
		}
	}
	o, err := loadOwner(ctx, s.DB, true)
	if err != nil {
		return "", nil, err
	}
	creation, data, err := s.WA.BeginRegistration(o, registrationOptions(o)...)
	if err != nil {
		return "", nil, fmt.Errorf("begin registration: %w", err)
	}
	c.data = *data
	id, err := s.putCeremony(c)
	if err != nil {
		return "", nil, err
	}
	return id, creation, nil
}

// Registered is the result of a finished registration.
type Registered struct {
	ID    int64
	Token string // raw session token; set only for the first enrollment, which logs the owner in
}

// FinishRegistration verifies the authenticator's response (body is the JSON the browser produced) and
// stores the credential. For the first enrollment the code is consumed in the same transaction.
func (s *Service) FinishRegistration(ctx context.Context, ceremonyID, label string, body []byte, sess *Session, ip string) (Registered, error) {
	label, err := cleanLabel(label)
	if err != nil {
		return Registered{}, err
	}
	now := s.now()
	if sess == nil && !s.limiter.allow(ip, now) {
		return Registered{}, ErrRateLimited
	}
	c, ok := s.takeCeremony(ceremonyID, true)
	if !ok || (c.codeID == 0 && (sess == nil || sess.ID != c.sessionID)) {
		return Registered{}, s.regFail(ctx, ip, "no_ceremony")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return Registered{}, s.regFail(ctx, ip, "malformed_response")
	}
	o, err := loadOwner(ctx, s.DB, false)
	if err != nil {
		return Registered{}, err
	}
	cred, err := s.WA.CreateCredential(o, c.data, parsed)
	if err != nil {
		return Registered{}, s.regFail(ctx, ip, protoReason(err))
	}
	var out Registered
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(credLockKey)); err != nil {
			return err
		}
		actor, action := "owner", "auth.passkey.add"
		if c.codeID != 0 {
			actor, action = "enroll", "auth.enroll"
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_credentials`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrAlreadyEnrolled
			}
			var used int64
			err := tx.QueryRow(ctx, `UPDATE auth_enroll_codes SET used_at = $2 WHERE id = $1 AND used_at IS NULL AND expires_at > $2 RETURNING id`,
				c.codeID, now).Scan(&used)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAuthFailed
			}
			if err != nil {
				return err
			}
		} else {
			actor = sess.Actor()
		}
		transports := make([]string, 0, len(cred.Transport))
		for _, t := range cred.Transport {
			transports = append(transports, string(t))
		}
		if err := tx.QueryRow(ctx, `INSERT INTO auth_credentials (credential_id, public_key, sign_count, transports, flags, attestation_type, aaguid, clone_warning, label)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			cred.ID, cred.PublicKey, int64(cred.Authenticator.SignCount), transports, int16(cred.Flags.ProtocolValue()), cred.AttestationType,
			cred.Authenticator.AAGUID, cred.Authenticator.CloneWarning, label).Scan(&out.ID); err != nil {
			return err
		}
		if err := recordSystem(ctx, tx, actor, action, map[string]any{"credential": out.ID, "label": label}); err != nil {
			return err
		}
		if c.codeID != 0 {
			id := out.ID
			tok, err := createSession(ctx, tx, now, s.cfg.MaxTTL, &id, "passkey")
			if err != nil {
				return err
			}
			out.Token = tok
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyEnrolled) || errors.Is(err, ErrAuthFailed) {
			return Registered{}, s.regFail(ctx, ip, "refused")
		}
		return Registered{}, err
	}
	s.limiter.reset(ip)
	return out, nil
}

func (s *Service) regFail(ctx context.Context, ip, reason string) error {
	s.limiter.fail(ip, s.now())
	s.auditFailure(ctx, "auth.enroll.failure", reason)
	return ErrAuthFailed
}

// protoReason maps a library error to a fixed, non-sensitive word.
func protoReason(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.Type != "" && len(pe.Type) < 40 {
		return pe.Type
	}
	return "verification_failed"
}

// BeginLogin starts a discoverable-credential login. It reveals nothing about stored credentials.
func (s *Service) BeginLogin(ip string) (string, *protocol.CredentialAssertion, error) {
	if !s.limiter.allow(ip, s.now()) {
		return "", nil, ErrRateLimited
	}
	assertion, data, err := s.WA.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, fmt.Errorf("begin login: %w", err)
	}
	id, err := s.putCeremony(&ceremony{data: *data})
	if err != nil {
		return "", nil, err
	}
	return id, assertion, nil
}

// FinishLogin verifies the assertion and, on success, returns a new raw session token.
func (s *Service) FinishLogin(ctx context.Context, ceremonyID string, body []byte, ip string) (string, error) {
	now := s.now()
	if !s.limiter.allow(ip, now) {
		return "", ErrRateLimited
	}
	fail := func(reason string) (string, error) {
		s.limiter.fail(ip, now)
		s.auditFailure(ctx, "auth.login.failure", reason)
		return "", ErrAuthFailed
	}
	c, ok := s.takeCeremony(ceremonyID, false)
	if !ok {
		return fail("no_ceremony")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return fail("malformed_response")
	}
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		o, err := loadOwner(ctx, s.DB, false)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(userHandle, o.handle) {
			return nil, errors.New("unknown user handle")
		}
		return o, nil
	}
	cred, err := s.WA.ValidateDiscoverableLogin(handler, c.data, parsed)
	if err != nil {
		return fail(protoReason(err))
	}
	if cred.Authenticator.CloneWarning {
		// The counter went backwards: the passkey may have been cloned. Persist the warning, refuse.
		if _, err := s.DB.Exec(ctx, `UPDATE auth_credentials SET clone_warning = true WHERE credential_id = $1`, cred.ID); err != nil {
			return "", err
		}
		return fail("clone_warning")
	}
	var token string
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `UPDATE auth_credentials SET sign_count = $2, flags = $3, last_used_at = $4 WHERE credential_id = $1 RETURNING id`,
			cred.ID, int64(cred.Authenticator.SignCount), int16(cred.Flags.ProtocolValue()), now).Scan(&id)
		if err != nil {
			return err
		}
		if token, err = createSession(ctx, tx, now, s.cfg.MaxTTL, &id, "passkey"); err != nil {
			return err
		}
		return recordSystem(ctx, tx, fmt.Sprintf("owner:passkey-%d", id), "auth.login.success", map[string]any{"credential": id})
	})
	if err != nil {
		return "", err
	}
	s.limiter.reset(ip)
	return token, nil
}

// Logout revokes the session.
func (s *Service) Logout(ctx context.Context, sess *Session) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, sess.ID, s.now()); err != nil {
			return err
		}
		return recordSystem(ctx, tx, sess.Actor(), "auth.logout", map[string]any{"session": sess.ID})
	})
}

// CredentialInfo is a stored passkey as shown to the owner. No key material.
type CredentialInfo struct {
	ID         int64      `json:"id"`
	Label      string     `json:"label"`
	Transports []string   `json:"transports"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (s *Service) ListCredentials(ctx context.Context) ([]CredentialInfo, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, label, transports, created_at, last_used_at FROM auth_credentials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CredentialInfo, error) {
		var c CredentialInfo
		err := r.Scan(&c.ID, &c.Label, &c.Transports, &c.CreatedAt, &c.LastUsedAt)
		return c, err
	})
}

// DeleteCredential removes a passkey and revokes its sessions. The last passkey cannot be removed.
func (s *Service) DeleteCredential(ctx context.Context, id int64, sess *Session) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(credLockKey)); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_credentials`).Scan(&n); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_credentials WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if n <= 1 {
			return ErrLastCredential
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE credential_id = $1 AND revoked_at IS NULL`, id).Scan(&live); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_credentials WHERE id = $1`, id); err != nil { // sessions go with it (ON DELETE CASCADE)
			return err
		}
		if err := recordSystem(ctx, tx, sess.Actor(), "auth.passkey.remove", map[string]any{"credential": id}); err != nil {
			return err
		}
		if live > 0 {
			return recordSystem(ctx, tx, sess.Actor(), "auth.session.revoke", map[string]any{"credential": id, "sessions": live})
		}
		return nil
	})
}
