package audit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/audit"
	"github.com/fiat/content-studio/internal/store/storetest"
)

func TestRecordIsAppendOnly(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{Actor: audit.Owner, Action: "test.action", TargetType: "thing", TargetID: 7,
			Detail: map[string]any{"ok": true, "n": 3, "nested": map[string]any{"secret": "x"}, "long": strings.Repeat("a", 500)}})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE audit_log SET action = 'edited'`,
		`DELETE FROM audit_log`,
		`TRUNCATE audit_log`,
	} {
		if _, err := db.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: err %v, want an append-only refusal", stmt, err)
		}
	}
	rows, err := audit.List(ctx, db, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v, want the one untouched row", rows, err)
	}
	r := rows[0]
	if r.Action != "test.action" || r.Actor != "owner" || r.TargetID == nil || *r.TargetID != 7 {
		t.Fatalf("row %+v", r)
	}
	if _, nested := r.Detail["nested"]; nested {
		t.Fatalf("non-scalar detail value was kept: %v", r.Detail)
	}
	if long, _ := r.Detail["long"].(string); len(long) > 210 {
		t.Fatalf("long detail value not truncated (%d bytes)", len(long))
	}
}

func TestRecordRollsBackWithItsTransaction(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{Actor: audit.Owner, Action: "test.action", TargetType: "thing"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := audit.List(ctx, db, 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows %v err %v, want none after rollback", rows, err)
	}
}

func TestRecordRequiresActorActionAndTarget(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	for _, e := range []audit.Entry{{Action: "a", TargetType: "t"}, {Actor: "o", TargetType: "t"}, {Actor: "o", Action: "a"}} {
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return audit.Record(ctx, tx, e) })
		if err == nil {
			t.Fatalf("entry %+v accepted", e)
		}
	}
}
