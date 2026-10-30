package api_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth/authtest"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// A server-wide WriteTimeout must not cut a video download that a slow client reads: artifact content
// gets Limits.StreamWrite instead. With a tiny StreamWrite the same download is cut, which proves the
// deadline (and not luck) is what lets the long one finish.
func TestServerWriteTimeoutDoesNotCutVideoStreaming(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789abcdef"), 4<<20) // 64 MiB, more than the socket buffers hold
	run := func(t *testing.T, stream time.Duration) (int, error) {
		db := storetest.New(t)
		st := &artifacts.Store{Root: t.TempDir()}
		s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
			Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, Limits: api.Limits{StreamWrite: stream},
			Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		s.Auth = authtest.Service(t, db)
		v := seedVideo(t, db, st, content, "qa_passed")
		ts := httptest.NewUnstartedServer(authtest.AutoLogin(s.Echo(), authtest.Session(t, s.Auth)))
		ts.Config.WriteTimeout = 300 * time.Millisecond
		ts.Start()
		defer ts.Close()
		resp, err := http.Get(ts.URL + "/api/artifacts/" + strconv.FormatInt(v.ID, 10) + "/content")
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		time.Sleep(900 * time.Millisecond) // a slow client: longer than WriteTimeout before it reads
		n, err := io.Copy(io.Discard, resp.Body)
		return int(n), err
	}
	n, err := run(t, 30*time.Second)
	if err != nil || n != len(content) {
		t.Fatalf("streaming with an extended deadline: %d of %d bytes, err %v", n, len(content), err)
	}
	n, err = run(t, time.Millisecond)
	if err == nil && n == len(content) {
		t.Fatal("control: a 1 ms write deadline did not cut the download, so the test proves nothing")
	}
}
