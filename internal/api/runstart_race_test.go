package api_test

import (
	"fmt"
	"testing"
)

func TestConcurrentRunStartsCreateExactlyOneRun(t *testing.T) {
	ts, db := multiServer(t)
	_, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("knowledge_shorts", 45))
	ep := int64(body["episode_id"].(float64))
	if code, body := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/sources", ep), "", srcBody("Sea facts", "", "น้ำทะเลมีความเค็ม")); code != 201 {
		t.Fatalf("add source: %d %v", code, body)
	}
	const n = 8
	codes := make(chan int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			code, _ := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", ep), "", "")
			codes <- code
		}()
	}
	close(start)
	created, conflicts := 0, 0
	for i := 0; i < n; i++ {
		switch code := <-codes; code {
		case 201:
			created++
		case 409:
			conflicts++
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("created %d, conflicts %d", created, conflicts)
	}
	if runs, jobs := count(t, db, `SELECT count(*) FROM workflow_runs WHERE episode_id = $1`, ep), count(t, db, `SELECT count(*) FROM jobs`); runs != 1 || jobs != 1 {
		t.Fatalf("%d runs, %d jobs", runs, jobs)
	}
}
