package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/store/storetest"
)

func TestResourceLimitValidation(t *testing.T) {
	ts, db := serverDB(t)
	ctx := context.Background()
	read := func() int {
		var n int
		if err := db.QueryRow(ctx, `SELECT max_concurrent FROM resource_limits WHERE resource = 'gpu'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, c := range map[string]struct {
		path, body string
		want       int
	}{
		"zero":                   {"/api/resources/gpu", `{"max_concurrent":0}`, 400},
		"negative":               {"/api/resources/gpu", `{"max_concurrent":-1}`, 400},
		"too large":              {"/api/resources/gpu", `{"max_concurrent":5,"confirm_unsafe":true}`, 400},
		"fraction":               {"/api/resources/gpu", `{"max_concurrent":1.5}`, 400},
		"string":                 {"/api/resources/gpu", `{"max_concurrent":"2"}`, 400},
		"missing":                {"/api/resources/gpu", `{}`, 400},
		"above safe, no confirm": {"/api/resources/gpu", `{"max_concurrent":2}`, 400},
		"unknown resource":       {"/api/resources/tpu", `{"max_concurrent":1}`, 404},
	} {
		if code, body := call(t, ts, "PUT", c.path, "", c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", name, code, body, c.want)
		}
	}
	if n := read(); n != 1 {
		t.Fatalf("a refused request changed the limit to %d", n)
	}
	wantActions(t, db)
	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM resource_limits`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("an unknown resource created a row: %d %v", rows, err)
	}
}

func TestResourceLimitChangeIsAuditedAndTakesEffectForClaims(t *testing.T) {
	ts, db := serverDB(t)
	ctx := context.Background()
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3, Owner: "w1", LeaseTTL: time.Minute}
	run1, err := q.CreateRun(ctx, ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	run2, err := q.CreateRun(ctx, ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := q.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx); err != jobs.ErrNoJob {
		t.Fatalf("second GPU claim with limit 1: %v", err)
	}
	code, body := call(t, ts, "GET", "/api/resources", "", "")
	gpu := body["resources"].([]any)[0].(map[string]any)
	if code != 200 || gpu["name"] != "gpu" || gpu["max_concurrent"] != 1.0 || gpu["running"] != 1.0 || gpu["waiting"] != 1.0 || gpu["safe_max"] != 1.0 || gpu["max"] != 4.0 {
		t.Fatalf("GET /api/resources: %d %v", code, body)
	}

	code, body = call(t, ts, "PUT", "/api/resources/gpu", "", `{"max_concurrent":2,"confirm_unsafe":true}`)
	if code != 200 || body["max_concurrent"] != 2.0 {
		t.Fatalf("%d %v", code, body)
	}
	wantActions(t, db, "resource_limit.update")
	var res string
	var from, to int
	var unsafe bool
	var actorName string
	if err := db.QueryRow(ctx, `SELECT actor, detail->>'resource', (detail->>'from')::int, (detail->>'to')::int, (detail->>'confirmed_unsafe')::bool FROM audit_log`).Scan(&actorName, &res, &from, &to, &unsafe); err != nil {
		t.Fatal(err)
	}
	if actorName == "" || res != "gpu" || from != 1 || to != 2 || !unsafe {
		t.Fatalf("audit actor=%q resource=%q from=%d to=%d unsafe=%v", actorName, res, from, to, unsafe)
	}

	// The waiting job is woken and claimed by the very next Claim: no restart, no queue change.
	j, err := q.Claim(ctx)
	if err != nil || j.RunID != run2 {
		t.Fatalf("claim after raising the limit: %+v %v (runs %d, %d)", j, err, run1, run2)
	}
	_, body = call(t, ts, "GET", "/api/resources", "", "")
	gpu = body["resources"].([]any)[0].(map[string]any)
	if gpu["running"] != 2.0 || gpu["waiting"] != 0.0 {
		t.Fatalf("usage after the claim: %v", gpu)
	}

	// Lowering back to 1 never stops running jobs; it is allowed without confirmation.
	if code, body := call(t, ts, "PUT", "/api/resources/gpu", "", `{"max_concurrent":1}`); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	var running int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state = 'running' AND resource = 'gpu'`).Scan(&running); err != nil || running != 2 {
		t.Fatalf("lowering the limit touched running jobs: %d %v", running, err)
	}
	wantActions(t, db, "resource_limit.update", "resource_limit.update")
}

func TestResourceLimitRollsBackWhenTheAuditRowCannotBeWritten(t *testing.T) {
	ts, db := serverDB(t)
	ctx := context.Background()
	breakAudit(t, db)
	if code, body := call(t, ts, "PUT", "/api/resources/gpu", "", `{"max_concurrent":2,"confirm_unsafe":true}`); code != 500 {
		t.Fatalf("%d %v, want 500", code, body)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT max_concurrent FROM resource_limits WHERE resource = 'gpu'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("limit changed without its audit row: %d %v", n, err)
	}
}

func TestPoliciesAreReadOnlyAndLocalOnly(t *testing.T) {
	ts, db := serverDB(t)
	code, body := call(t, ts, "GET", "/api/policies", "", "")
	if code != 200 || body["cloud_enabled"] != false || body["changeable"] != false {
		t.Fatalf("%d %v", code, body)
	}
	tasks := body["tasks"].([]any)
	if len(tasks) != 4 {
		t.Fatalf("tasks: %v", tasks)
	}
	for _, x := range tasks {
		if p := x.(map[string]any)["policy"]; p != "LOCAL_ONLY" {
			t.Fatalf("task %v has policy %v", x, p)
		}
	}
	// There is no way to change a policy: no write route exists.
	for _, m := range []string{"PUT", "POST", "PATCH", "DELETE"} {
		if code, _ := call(t, ts, m, "/api/policies", "", `{"policy":"CLOUD_PREFERRED"}`); code != 404 && code != 405 {
			t.Errorf("%s /api/policies returned %d", m, code)
		}
	}
	wantActions(t, db)
}
