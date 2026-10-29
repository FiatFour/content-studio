// Package audit writes the append-only audit log. Callers pass the transaction of the action being
// audited, so the row exists if and only if the action committed. The table refuses UPDATE, DELETE
// and TRUNCATE in the database itself (migrations/0010_audit_log.sql).
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Owner is the only actor until authentication lands (D9).
const Owner = "owner"

const maxString = 200

// Entry is one audited action. Detail must hold short, non-secret scalars (string, bool, numbers);
// anything else is dropped. Never put request bodies, secrets or absolute paths in it.
type Entry struct {
	Actor      string
	Action     string
	TargetType string
	TargetID   int64 // 0 means no target row
	Detail     map[string]any
}

// Record inserts one row using tx. It must be called inside the transaction of the audited action.
func Record(ctx context.Context, tx pgx.Tx, e Entry) error {
	if e.Actor == "" || e.Action == "" || e.TargetType == "" {
		return errors.New("audit: actor, action and target type are required")
	}
	detail, err := json.Marshal(sanitize(e.Detail))
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	var target *int64
	if e.TargetID != 0 {
		target = &e.TargetID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, target_type, target_id, detail) VALUES ($1, $2, $3, $4, $5)`,
		e.Actor, e.Action, e.TargetType, target, detail); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

func sanitize(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		switch x := v.(type) {
		case string:
			if utf8.RuneCountInString(x) > maxString {
				x = string([]rune(x)[:maxString]) + "..."
			}
			out[k] = x
		case bool, int, int32, int64, float64:
			out[k] = x
		}
	}
	return out
}

// Row is one audit log entry as returned by List.
type Row struct {
	ID         int64          `json:"id"`
	At         time.Time      `json:"at"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   *int64         `json:"target_id"`
	Detail     map[string]any `json:"detail"`
}

// Lister is satisfied by *pgxpool.Pool.
type Lister interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// List returns up to limit rows newest first, starting below beforeID (0 means from the newest).
func List(ctx context.Context, db Lister, beforeID int64, limit int) ([]Row, error) {
	rows, err := db.Query(ctx, `SELECT id, at, actor, action, target_type, target_id, detail FROM audit_log
		WHERE ($1::bigint = 0 OR id < $1) ORDER BY id DESC LIMIT $2`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var x Row
		err := r.Scan(&x.ID, &x.At, &x.Actor, &x.Action, &x.TargetType, &x.TargetID, &x.Detail)
		return x, err
	})
}
