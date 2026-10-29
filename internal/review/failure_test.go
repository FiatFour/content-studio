package review_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/review"
)

// TestApprovalIsBoundToTheExactArtifactHash is the M2 "unauthorized approval attempt" case as far
// as it can be tested today. There is no authentication until decision D9, so "who may approve"
// is PENDING D9 and not tested here. What is guaranteed now: a decision only lands on the exact
// bytes the reviewer saw. A wrong, other-version, truncated or differently-cased hash is refused,
// and a refusal leaves no approval row and nothing exportable.
func TestApprovalIsBoundToTheExactArtifactHash(t *testing.T) {
	f, q, ep, project := setup(t)
	ctx := context.Background()
	v1 := f.video(t, q, ep, project, "video-v1", "qa_passed")
	v2 := f.video(t, q, ep, project, "video-v2", "qa_passed")

	attempts := map[string]string{
		"other version's hash": v1.SHA256,
		"made-up hash":         strings.Repeat("0", 64),
		"truncated hash":       v2.SHA256[:12],
		"upper-cased hash":     strings.ToUpper(v2.SHA256),
		"empty hash":           "",
	}
	for name, hash := range attempts {
		if _, err := f.svc.Decide(ctx, v2.ID, hash, review.Approved, "", "intruder"); !refused(err) {
			t.Errorf("%s: approval not refused: %v", name, err)
		}
	}
	if ap, err := f.svc.Latest(ctx, v2.ID); err != nil || ap != nil {
		t.Fatalf("a refused attempt left an approval: %+v %v", ap, err)
	}
	if _, err := f.svc.Export(ctx, v2.ID, "intruder"); !refused(err) {
		t.Fatalf("export of a never-approved version: %v", err)
	}
	var n int
	if err := f.svc.DB.QueryRow(ctx, `SELECT count(*) FROM approvals`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("approvals rows %d err %v", n, err)
	}

	// Approving v2 with its own hash works, and then swapping the bytes under the same row
	// still cannot be exported.
	if _, err := f.svc.Decide(ctx, v2.ID, v2.SHA256, review.Approved, "", "owner"); err != nil {
		t.Fatal(err)
	}
	path, _ := f.store.Abs(v2.StoragePath)
	if err := os.WriteFile(path, []byte("swapped after approval"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Export(ctx, v2.ID, "owner"); !refused(err) {
		t.Fatalf("export of swapped bytes: %v", err)
	}
	// And v1 was never approved by v2's approval.
	if ap, _ := f.svc.Latest(ctx, v1.ID); ap != nil {
		t.Fatal("v2's approval attached to v1")
	}
}

// TestExportRefusedWhenApprovedFileIsMissing covers the missing-media case at export time: the
// approved row exists, the file does not.
func TestExportRefusedWhenApprovedFileIsMissing(t *testing.T) {
	f, q, ep, project := setup(t)
	ctx := context.Background()
	v := f.video(t, q, ep, project, "video-v1", "qa_passed")
	if _, err := f.svc.Decide(ctx, v.ID, v.SHA256, review.Approved, "", "owner"); err != nil {
		t.Fatal(err)
	}
	path, _ := f.store.Abs(v.StoragePath)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Export(ctx, v.ID, "owner"); !refused(err) {
		t.Fatalf("export with the file gone: %v", err)
	}
	var n int
	if err := f.svc.DB.QueryRow(ctx, `SELECT count(*) FROM exports`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("exports rows %d err %v", n, err)
	}
	if entries, _ := os.ReadDir(f.svc.ExportDir); len(entries) != 0 {
		t.Fatalf("export dir has %d files", len(entries))
	}
}
