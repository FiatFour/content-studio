// Package authtest gives tests an authenticated session without any bypass in production code: it
// creates a real session row through auth.Service, exactly as a login would, and hands back the token.
package authtest

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/auth"
)

// Origin is the one allowed browser origin in tests.
const Origin = "http://localhost:8080"

// Service returns an auth.Service for RP ID "localhost" and Origin.
func Service(t testing.TB, db *pgxpool.Pool) *auth.Service {
	t.Helper()
	svc, err := auth.New(db, auth.Config{RPID: "localhost", Origins: []string{Origin}})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// Session creates a test session and returns its raw token.
func Session(t testing.TB, svc *auth.Service) string {
	t.Helper()
	tok, err := svc.CreateSession(context.Background(), nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// AutoLogin wraps h so every request that has no session cookie / Origin gets the given ones. It lets
// the existing endpoint tests stay about their endpoint; the real middleware still validates the
// cookie against the database. Tests of authentication itself must not use it.
func AutoLogin(h http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(auth.CookieName); err != nil {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		}
		if r.Header.Get("Origin") == "" {
			r.Header.Set("Origin", Origin)
		}
		h.ServeHTTP(w, r)
	})
}
