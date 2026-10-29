package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/auth"
)

// publicPaths are the only /api routes reachable without a session (method + exact path). Everything
// else under /api, including paths that do not exist, answers 401. The enroll routes decide for
// themselves: they need a session, or a one-time code while no passkey exists.
var publicPaths = map[string]bool{
	"GET /api/health":              true,
	"GET /api/auth/status":         true,
	"POST /api/auth/login/begin":   true,
	"POST /api/auth/login/finish":  true,
	"POST /api/auth/enroll/begin":  true,
	"POST /api/auth/enroll/finish": true,
}

const actorKey = "actor"
const sessionKey = "session"

// actor is the authenticated identity for the audit log. Empty when there is none, which makes
// audit.Record refuse, so a handler that somehow runs unauthenticated fails closed.
func actor(c echo.Context) string {
	a, _ := c.Get(actorKey).(string)
	return a
}

func isAPI(p string) bool { return p == "/api" || strings.HasPrefix(p, "/api/") }

func stateChanging(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete
}

// originGuard is the minimal CSRF defense until #17: every state-changing request under /api must carry
// an Origin that is configured, and must not be marked cross-site by the browser. Non-browser clients
// have to send an Origin header too.
func (s *Server) originGuard(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		r := c.Request()
		if !isAPI(r.URL.Path) || !stateChanging(r.Method) {
			return next(c)
		}
		if !s.originAllowed(r.Header.Get("Origin")) {
			return fail(http.StatusForbidden, "forbidden")
		}
		if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
			return fail(http.StatusForbidden, "forbidden")
		}
		return next(c)
	}
}

func (s *Server) originAllowed(origin string) bool {
	if origin == "" || s.Auth == nil {
		return false
	}
	for _, o := range s.Auth.Origins() {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// requireSession is default-deny for /api. The reason for a refusal is never revealed.
func (s *Server) requireSession(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		r := c.Request()
		if !isAPI(r.URL.Path) {
			return next(c)
		}
		if s.Auth != nil {
			if ck, err := r.Cookie(auth.CookieName); err == nil {
				if sess, err := s.Auth.Authenticate(r.Context(), ck.Value); err == nil {
					c.Set(sessionKey, sess)
					c.Set(actorKey, sess.Actor())
					return next(c)
				} else if !errors.Is(err, auth.ErrUnauthenticated) {
					return err
				}
			}
		}
		if publicPaths[r.Method+" "+r.URL.Path] {
			return next(c)
		}
		return fail(http.StatusUnauthorized, "unauthorized")
	}
}

func session(c echo.Context) *auth.Session {
	s, _ := c.Get(sessionKey).(*auth.Session)
	return s
}

// needSession is for handlers that are public to the middleware but still require a session.
func needSession(c echo.Context) (*auth.Session, error) {
	if s := session(c); s != nil {
		return s, nil
	}
	return nil, fail(http.StatusUnauthorized, "unauthorized")
}

func clientIP(c echo.Context) string {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		return c.Request().RemoteAddr
	}
	return host
}

// secureCookie: the cookie is Secure when the request arrived over TLS or from a configured https
// origin (a TLS-terminating proxy). Plain http on localhost keeps working.
func (s *Server) secureCookie(c echo.Context) bool {
	if c.IsTLS() {
		return true
	}
	o := c.Request().Header.Get("Origin")
	if u, err := url.Parse(o); err == nil && u.Scheme == "https" {
		return s.originAllowed(o)
	}
	return false
}

func (s *Server) setSessionCookie(c echo.Context, token string) {
	c.SetCookie(&http.Cookie{Name: auth.CookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.secureCookie(c), MaxAge: int(s.Auth.MaxAge() / time.Second)})
}

func (s *Server) clearSessionCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{Name: auth.CookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.secureCookie(c), MaxAge: -1})
}

func readBody(c echo.Context) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 64<<10))
	if err != nil {
		return nil, fail(http.StatusBadRequest, "invalid request body")
	}
	return b, nil
}

func (s *Server) authError(err error) error {
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		return fail(http.StatusTooManyRequests, "too many attempts, try again later")
	case errors.Is(err, auth.ErrAlreadyEnrolled):
		return fail(http.StatusForbidden, "a passkey is already enrolled; sign in to add another")
	case errors.Is(err, auth.ErrAuthFailed):
		return fail(http.StatusUnauthorized, "authentication failed")
	case errors.Is(err, auth.ErrInvalidLabel):
		return fail(http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrLastCredential):
		return fail(http.StatusConflict, "cannot remove the last passkey")
	case errors.Is(err, auth.ErrNotFound):
		return fail(http.StatusNotFound, "not found")
	}
	return err
}

func (s *Server) authStatus(c echo.Context) error {
	enrolled, err := s.Auth.Enrolled(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"enrolled": enrolled, "authenticated": session(c) != nil})
}

func (s *Server) loginBegin(c echo.Context) error {
	id, assertion, err := s.Auth.BeginLogin(clientIP(c))
	if err != nil {
		return s.authError(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"ceremony_id": id, "options": assertion})
}

type finishReq struct {
	CeremonyID string          `json:"ceremony_id"`
	Label      string          `json:"label"`
	Credential json.RawMessage `json:"credential"`
}

func readFinish(c echo.Context) (finishReq, error) {
	var req finishReq
	b, err := readBody(c)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(b, &req); err != nil || req.CeremonyID == "" || len(req.Credential) == 0 {
		return req, fail(http.StatusBadRequest, "invalid request body")
	}
	return req, nil
}

func (s *Server) loginFinish(c echo.Context) error {
	req, err := readFinish(c)
	if err != nil {
		return err
	}
	token, err := s.Auth.FinishLogin(c.Request().Context(), req.CeremonyID, req.Credential, clientIP(c))
	if err != nil {
		return s.authError(err)
	}
	s.setSessionCookie(c, token)
	return c.JSON(http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) enrollBegin(c echo.Context) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	id, creation, err := s.Auth.BeginRegistration(c.Request().Context(), req.Code, session(c), clientIP(c))
	if err != nil {
		return s.authError(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"ceremony_id": id, "options": creation})
}

func (s *Server) enrollFinish(c echo.Context) error {
	req, err := readFinish(c)
	if err != nil {
		return err
	}
	res, err := s.Auth.FinishRegistration(c.Request().Context(), req.CeremonyID, req.Label, req.Credential, session(c), clientIP(c))
	if err != nil {
		return s.authError(err)
	}
	if res.Token != "" {
		s.setSessionCookie(c, res.Token)
	}
	return c.JSON(http.StatusCreated, map[string]any{"id": res.ID, "authenticated": res.Token != "" || session(c) != nil})
}

func (s *Server) logout(c echo.Context) error {
	sess, err := needSession(c)
	if err != nil {
		return err
	}
	if err := s.Auth.Logout(c.Request().Context(), sess); err != nil {
		return err
	}
	s.clearSessionCookie(c)
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listCredentials(c echo.Context) error {
	creds, err := s.Auth.ListCredentials(c.Request().Context())
	if err != nil {
		return err
	}
	cur := session(c)
	return c.JSON(http.StatusOK, map[string]any{"credentials": creds, "current_credential_id": cur.CredentialID})
}

func (s *Server) deleteCredential(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	if err := s.Auth.DeleteCredential(c.Request().Context(), id, session(c)); err != nil {
		return s.authError(err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}
