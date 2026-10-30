package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalClientNeverFollowsRedirects(t *testing.T) {
	var hits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer elsewhere.Close()
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer svc.Close()
	_, err := get(context.Background(), NewLocalClient(5*time.Second), svc.URL)
	if err == nil {
		t.Fatal("a redirect was treated as success")
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect target was contacted")
	}
}
