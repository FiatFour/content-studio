package canon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/store/storetest"
)

const owner = "owner:passkey-1"

const miaPayload = `{"slug":"mia","name":"Mia","appearance":"short black hair, yellow hoodie","voice":"bright, fast talker"}`

func propose(t *testing.T, db *pgxpool.Pool, pid int64, kind, payload string) int64 {
	t.Helper()
	id, err := Create(context.Background(), db, NewProposal{ProjectID: pid, Kind: kind, Payload: json.RawMessage(payload), ProposedBy: "stage:script"})
	if err != nil {
		t.Fatalf("create %s: %v", kind, err)
	}
	return id
}

func count(t *testing.T, db *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateWritesNoCanonAndNeedsApproval(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	id := propose(t, db, pid, KindCharacterCreate, miaPayload)
	if n := count(t, db, `SELECT count(*) FROM characters`); n != 0 {
		t.Fatalf("proposal wrote %d characters", n)
	}
	p, err := Get(ctx, db, pid, id)
	if err != nil || p.Status != StatusProposed || p.DecidedBy != nil || p.ProposedBy != "stage:script" {
		t.Fatalf("get: %v %+v", err, p)
	}
	if err := Approve(ctx, db, pid, id, owner, "fits the premise"); err != nil {
		t.Fatal(err)
	}
	p, _ = Get(ctx, db, pid, id)
	if p.Status != StatusApproved || *p.DecidedBy != owner || *p.Reason != "fits the premise" || p.DecidedAt == nil {
		t.Fatalf("decision: %+v", p)
	}
	if n := count(t, db, `SELECT count(*) FROM characters WHERE slug = 'mia'`); n != 1 {
		t.Fatalf("approved character missing: %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM audit_log WHERE action = 'canon.proposal.approved' AND actor = $1 AND target_id = $2`, owner, id); n != 1 {
		t.Fatalf("approve audit rows: %d", n)
	}
}

func TestDoubleDecisionIsRefused(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	id := propose(t, db, pid, KindFactAdd, `{"statement":"Mia has a green bike"}`)
	if err := Approve(ctx, db, pid, id, owner, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := Approve(ctx, db, pid, id, owner, "again"); !errors.Is(err, ErrDecided) {
		t.Fatalf("double approve: %v", err)
	}
	if err := Reject(ctx, db, pid, id, owner, "changed my mind"); !errors.Is(err, ErrDecided) {
		t.Fatalf("reject after approve: %v", err)
	}
	if n := count(t, db, `SELECT count(*) FROM established_facts`); n != 1 {
		t.Fatalf("facts: %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM canon_proposal_decisions`); n != 1 {
		t.Fatalf("decisions: %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM audit_log WHERE action LIKE 'canon.proposal.%' AND action <> 'canon.proposal.create'`); n != 1 {
		t.Fatalf("decision audit rows: %d", n)
	}
}

func TestRejectChangesNothingButAudit(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	id := propose(t, db, pid, KindCharacterCreate, miaPayload)
	if err := Reject(ctx, db, pid, id, owner, "off model"); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT count(*) FROM characters`) != 0 {
		t.Fatal("rejected proposal changed canon")
	}
	p, _ := Get(ctx, db, pid, id)
	if p.Status != StatusRejected || *p.Reason != "off model" {
		t.Fatalf("%+v", p)
	}
	if n := count(t, db, `SELECT count(*) FROM audit_log WHERE action = 'canon.proposal.rejected' AND actor = $1`, owner); n != 1 {
		t.Fatalf("reject audit rows: %d", n)
	}
	if err := Approve(ctx, db, pid, id, owner, "ok"); !errors.Is(err, ErrDecided) {
		t.Fatalf("approve after reject: %v", err)
	}
}

func TestApproveIsAtomicWithAudit(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	// Relationship to a character that does not exist: the commit fails, so the decision and audit roll back.
	id := propose(t, db, pid, KindRelationshipAdd, `{"from":"mia","to":"bob","type":"friend"}`)
	if err := Approve(ctx, db, pid, id, owner, "ok"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if count(t, db, `SELECT count(*) FROM canon_proposal_decisions`) != 0 || count(t, db, `SELECT count(*) FROM character_relationships`) != 0 ||
		count(t, db, `SELECT count(*) FROM audit_log WHERE action = 'canon.proposal.approved'`) != 0 {
		t.Fatal("failed approve left partial state")
	}
	if p, _ := Get(ctx, db, pid, id); p.Status != StatusProposed {
		t.Fatalf("status %s", p.Status)
	}
	// If the audit insert fails, the canon change rolls back too. An oversized actor is not enough
	// (the audit table has no limit), so force the failure with an empty-actor refusal path instead.
	if err := Approve(ctx, db, pid, id, "", "ok"); err == nil {
		t.Fatal("empty actor must be refused")
	}
	for _, slug := range []string{"mia", "bob"} {
		p := strings.Replace(miaPayload, "mia", slug, 1)
		if err := Approve(ctx, db, pid, propose(t, db, pid, KindCharacterCreate, p), owner, "ok"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Approve(ctx, db, pid, id, owner, "now both exist"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM character_relationships r JOIN canon_proposal_decisions d ON d.proposal_id = r.proposal_id WHERE d.decided_by = $1`, owner); n != 1 {
		t.Fatalf("relationship rows: %d", n)
	}
}

func TestAuditFailureRollsBackCanonChange(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	id := propose(t, db, pid, KindCharacterCreate, miaPayload)
	if _, err := db.Exec(ctx, `ALTER TABLE audit_log ADD CONSTRAINT audit_test_block CHECK (action NOT LIKE 'canon.proposal.approved')`); err != nil {
		t.Fatal(err)
	}
	if err := Approve(ctx, db, pid, id, owner, "ok"); err == nil {
		t.Fatal("approve must fail when the audit row cannot be written")
	}
	if count(t, db, `SELECT count(*) FROM characters`) != 0 || count(t, db, `SELECT count(*) FROM canon_proposal_decisions`) != 0 {
		t.Fatal("canon change or decision survived a failed audit write")
	}
}

func TestDirectCanonWritesAreRefusedWithoutApproval(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	rejected := propose(t, db, pid, KindFactAdd, `{"statement":"Mia can fly"}`)
	if err := Reject(ctx, db, pid, rejected, owner, "no"); err != nil {
		t.Fatal(err)
	}
	pending := propose(t, db, pid, KindFactAdd, `{"statement":"Mia can swim"}`)
	for _, id := range []int64{pending, rejected, 99999} {
		_, err := db.Exec(ctx, `INSERT INTO established_facts (project_id, statement, proposal_id) VALUES ($1, 'Mia can fly high', $2)`, pid, id)
		if err == nil {
			t.Fatalf("direct fact insert with proposal %d was accepted", id)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO character_relationships (project_id, from_character, to_character, rel_type, proposal_id) VALUES ($1,1,2,'friend',$2)`, pid, pending); err == nil {
		t.Fatal("direct relationship insert was accepted")
	}
	// Approved rows and history are immutable.
	ok := propose(t, db, pid, KindFactAdd, `{"statement":"Mia has a green bike"}`)
	if err := Approve(ctx, db, pid, ok, owner, "ok"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE established_facts SET statement = 'something else'`, `DELETE FROM established_facts`,
		`UPDATE canon_proposals SET rationale = 'x'`, `DELETE FROM canon_proposals`,
		`UPDATE canon_proposal_decisions SET decision = 'rejected'`, `DELETE FROM canon_proposal_decisions`,
		`TRUNCATE canon_proposal_decisions CASCADE`,
	} {
		if _, err := db.Exec(ctx, q); err == nil {
			t.Errorf("%q was accepted", q)
		}
	}
}

func TestPayloadsAreValidatedAndCharacterUpdateMerges(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	bad := []struct{ kind, payload string }{
		{KindCharacterCreate, `{"slug":"Mia","name":"Mia","appearance":"short black hair","voice":"bright voice"}`},
		{KindCharacterCreate, miaPayload[:len(miaPayload)-1] + `,"lora_name":"x"}`}, // unknown field
		{KindCharacterCreate, miaPayload + ` {}`},
		{KindCharacterCreate, `{"slug":"mia","name":"Mia","appearance":"short black hair","voice":"bright voice","status":"deceased"}`},
		{KindCharacterUpdate, `{"slug":"mia"}`},
		{KindCharacterUpdate, `{"slug":"mia","status":"ghost"}`},
		{KindRelationshipAdd, `{"from":"mia","to":"mia","type":"friend"}`},
		{KindRelationshipAdd, `{"from":"mia","to":"bob","type":"overlord"}`},
		{KindRelationshipAdd, `{"from":"mia","to":"bob","type":"friend","starts_on":"2025-02-01","ends_on":"2025-01-01"}`},
		{KindFactAdd, `{"statement":"no"}`},
		{"locations.add", `{}`},
		{KindFactAdd, `not json`},
	}
	for _, b := range bad {
		_, err := Create(ctx, db, NewProposal{ProjectID: pid, Kind: b.kind, Payload: json.RawMessage(b.payload), ProposedBy: "stage:script"})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s %s: got %v, want ErrInvalid", b.kind, b.payload, err)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM canon_proposals`); n != 0 {
		t.Fatalf("invalid proposals stored: %d", n)
	}
	if _, err := Create(ctx, db, NewProposal{ProjectID: pid, Kind: KindFactAdd, Payload: json.RawMessage(`{"statement":"Mia has a bike"}`)}); err == nil {
		t.Fatal("empty proposed_by must be refused")
	}

	if err := Approve(ctx, db, pid, propose(t, db, pid, KindCharacterCreate, miaPayload), owner, "ok"); err != nil {
		t.Fatal(err)
	}
	// Duplicate slug: conflict at approval, proposal stays open.
	dup := propose(t, db, pid, KindCharacterCreate, miaPayload)
	if err := Approve(ctx, db, pid, dup, owner, "ok"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate slug: %v", err)
	}
	upd := propose(t, db, pid, KindCharacterUpdate, `{"slug":"mia","status":"moved_away","status_since":"2025-06-01","personality":"calmer"}`)
	if err := Approve(ctx, db, pid, upd, owner, "story beat"); err != nil {
		t.Fatal(err)
	}
	chars, err := Characters(ctx, db, pid)
	if err != nil || len(chars) != 1 || chars[0].Status != StatusMovedAway || chars[0].Personality != "calmer" || chars[0].Name != "Mia" || chars[0].StatusSince == nil {
		t.Fatalf("merge: %v %+v", err, chars)
	}
	// An update that is only invalid once merged (deceased needs a date) fails at approval and changes nothing.
	badMerge := propose(t, db, pid, KindCharacterUpdate, `{"slug":"mia","status":"deceased","status_since":""}`)
	if err := Approve(ctx, db, pid, badMerge, owner, "ok"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("merged invalid: %v", err)
	}
	if chars, _ = Characters(ctx, db, pid); chars[0].Status != StatusMovedAway {
		t.Fatalf("failed update changed canon: %+v", chars[0])
	}
}

func TestListFiltersByStatus(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	a := propose(t, db, pid, KindFactAdd, `{"statement":"fact number one"}`)
	b := propose(t, db, pid, KindFactAdd, `{"statement":"fact number two"}`)
	propose(t, db, pid, KindFactAdd, `{"statement":"fact number three"}`)
	if err := Reject(ctx, db, pid, a, owner, "no"); err != nil {
		t.Fatal(err)
	}
	if err := Approve(ctx, db, pid, b, owner, "yes"); err != nil {
		t.Fatal(err)
	}
	for status, want := range map[string]int{"": 3, StatusProposed: 1, StatusApproved: 1, StatusRejected: 1} {
		got, err := List(ctx, db, pid, status, 50)
		if err != nil || len(got) != want {
			t.Errorf("list %q: %v len %d want %d", status, err, len(got), want)
		}
	}
	if _, err := List(ctx, db, pid, "bogus", 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bogus status: %v", err)
	}
}
