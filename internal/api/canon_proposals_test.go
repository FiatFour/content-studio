package api_test

import (
	"context"
	"fmt"
	"testing"
)

func TestCanonProposalHTTPFlow(t *testing.T) {
	g := newRig(t)
	tok := g.sessionToken(t)
	post := func(path, body string) resp { return g.do(t, "POST", path, body, opt{token: tok}) }
	count := func(q string, args ...any) int {
		var n int
		if err := g.db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Creating a proposal needs a session and a valid payload, and writes no canon.
	if r := g.do(t, "POST", "/api/canon/proposals", `{}`, opt{}); r.code != 401 {
		t.Fatalf("no session: %d", r.code)
	}
	if r := post("/api/canon/proposals", `{"kind":"fact.add","payload":{"statement":"x"}}`); r.code != 400 {
		t.Fatalf("invalid payload: %d %s", r.code, r.body)
	}
	if r := post("/api/canon/proposals", `{"kind":"fact.add","payload":{"statement":"Mia has a bike","extra":1}}`); r.code != 400 {
		t.Fatalf("unknown field: %d", r.code)
	}
	r := post("/api/canon/proposals", `{"kind":"character.create","rationale":"new neighbour","payload":{"slug":"mia","name":"Mia","appearance":"short black hair","voice":"bright voice"}}`)
	if r.code != 201 {
		t.Fatalf("create: %d %s", r.code, r.body)
	}
	id := int64(r.json()["id"].(float64))
	if count(`SELECT count(*) FROM characters`) != 0 {
		t.Fatal("create wrote canon")
	}

	r = g.do(t, "GET", fmt.Sprintf("/api/canon/proposals/%d", id), "", opt{token: tok})
	if r.code != 200 || r.json()["status"] != "proposed" || r.json()["decided_by"] != nil {
		t.Fatalf("get: %d %s", r.code, r.body)
	}
	r = g.do(t, "GET", "/api/canon/proposals?status=proposed", "", opt{token: tok})
	if r.code != 200 || len(r.json()["proposals"].([]any)) != 1 {
		t.Fatalf("list: %d %s", r.code, r.body)
	}
	if r := g.do(t, "GET", "/api/canon/proposals?status=bogus", "", opt{token: tok}); r.code != 400 {
		t.Fatalf("bad status: %d", r.code)
	}

	// State-changing routes keep the Origin check and need a reason.
	path := fmt.Sprintf("/api/canon/proposals/%d/approve", id)
	if r := g.do(t, "POST", path, `{"reason":"ok"}`, opt{token: tok, origin: "https://evil.example"}); r.code != 403 {
		t.Fatalf("foreign origin: %d", r.code)
	}
	if r := post(path, `{}`); r.code != 400 {
		t.Fatalf("missing reason: %d", r.code)
	}
	if count(`SELECT count(*) FROM characters`) != 0 {
		t.Fatal("refused approvals wrote canon")
	}

	if r := post(path, `{"reason":"fits the show"}`); r.code != 200 {
		t.Fatalf("approve: %d %s", r.code, r.body)
	}
	if r := post(path, `{"reason":"again"}`); r.code != 409 {
		t.Fatalf("double approve: %d", r.code)
	}
	if r := post(fmt.Sprintf("/api/canon/proposals/%d/reject", id), `{"reason":"too late"}`); r.code != 409 {
		t.Fatalf("reject after approve: %d", r.code)
	}
	if count(`SELECT count(*) FROM characters WHERE slug = 'mia'`) != 1 {
		t.Fatal("approved character missing")
	}
	// The approver is the session identity, never something the client sends.
	var by string
	if err := g.db.QueryRow(context.Background(), `SELECT decided_by FROM canon_proposal_decisions WHERE proposal_id = $1`, id).Scan(&by); err != nil || by == "" || by == "owner" {
		t.Fatalf("decided_by %q %v", by, err)
	}
	if count(`SELECT count(*) FROM audit_log WHERE action = 'canon.proposal.approved' AND actor = $1`, by) != 1 {
		t.Fatal("approve audit row missing")
	}

	// Reject path.
	r = post("/api/canon/proposals", `{"kind":"fact.add","payload":{"statement":"Mia is a wizard"}}`)
	id2 := int64(r.json()["id"].(float64))
	if r := post(fmt.Sprintf("/api/canon/proposals/%d/reject", id2), `{"reason":"not our show"}`); r.code != 200 {
		t.Fatalf("reject: %d %s", r.code, r.body)
	}
	if count(`SELECT count(*) FROM established_facts`) != 0 {
		t.Fatal("rejected proposal changed canon")
	}
	if r := g.do(t, "GET", "/api/canon/proposals/99999", "", opt{token: tok}); r.code != 404 {
		t.Fatalf("unknown: %d", r.code)
	}
}
