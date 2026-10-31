package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/pipeline"
)

func TestReviewOfAHistoryVideoListsSourcesClaimsCertaintyAndFindings(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("history-video-bytes"), "qa_passed")
	src := seedSource(t, db, st, v, "s1", "Temple chronicle", "the temple was built in 1782. why the town emptied is unclear.")
	cc := pipeline.ClaimCheck{OK: true, Claims: 2, Beats: 2, Failures: []pipeline.ClaimFailure{}}
	raw, err := json.Marshal(pipeline.HistoryScript{Title: "t", Language: "th",
		Beats: []pipeline.ShortsBeat{
			{Narration: "สร้างปี 1782", ClaimIDs: []string{"c1"}},
			{Narration: "เมืองร้างเพราะภัยพิบัติ ประชากร 9999", ClaimIDs: []string{"c2"}}, // no hedge, number in no source
		},
		Claims: []pipeline.HistoryClaim{
			{ID: "c1", Text: "built", Certainty: "documented", DateText: "1782", Sources: []pipeline.HistorySource{{SourceID: "s1", Quote: "built in 1782"}}},
			{ID: "c2", Text: "emptied", Certainty: "speculation", Sources: []pipeline.HistorySource{{SourceID: "s1", Quote: "is unclear"}}},
		}, ClaimCheck: &cc})
	if err != nil {
		t.Fatal(err)
	}
	seedScriptFor(t, db, st, v, raw, map[string]any{"profile": "history_mystery",
		"sources": []any{map[string]any{"source_id": "s1", "artifact_id": src.ID, "version": 1, "sha256": src.SHA256}}})

	code, body := call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/review", "", "")
	if code != http.StatusOK {
		t.Fatalf("review: %d %v", code, body)
	}
	if _, has := body["shorts"]; has {
		t.Fatal("a history review must not have a shorts block")
	}
	h, _ := body["history"].(map[string]any)
	if h == nil {
		t.Fatalf("no history block: %v", body)
	}
	srcs, _ := h["sources"].([]any)
	s0, _ := srcs[0].(map[string]any)
	if len(srcs) != 1 || s0["source_id"] != "s1" || s0["title"] != "Temple chronicle" || s0["sha256"] != src.SHA256 {
		t.Fatalf("sources %v", srcs)
	}
	claims, _ := h["claims"].([]any)
	c1, _ := claims[1].(map[string]any)
	if len(claims) != 2 || c1["certainty"] != "speculation" || claims[0].(map[string]any)["certainty"] != "documented" || claims[0].(map[string]any)["date_text"] != "1782" {
		t.Fatalf("claims %v", claims)
	}
	if n := h["certainty_counts"].(map[string]any); n["documented"].(float64) != 1 || n["speculation"].(float64) != 1 {
		t.Fatalf("certainty_counts %v", n)
	}
	if beats, _ := h["claim_beats"].(map[string]any)["c2"].([]any); len(beats) != 1 || beats[0].(float64) != 2 {
		t.Fatalf("claim_beats %v", h["claim_beats"])
	}
	if h["stills_kind"] != "illustration" {
		t.Fatalf("stills_kind %v", h["stills_kind"])
	}
	// The unhedged speculation is a blocking finding and the source-less number a warning.
	got := map[string]string{}
	for _, f := range h["findings"].([]any) {
		m := f.(map[string]any)
		got[m["code"].(string)] = m["severity"].(string)
		if strings.Contains(m["message"].(string), "is unclear") {
			t.Errorf("finding echoes a quote: %v", m)
		}
	}
	if got[pipeline.HistoryUnlabelledUncertainty] != "blocking" || got[pipeline.HistoryNumberNotInSource] != "warning" || len(got) != 2 {
		t.Fatalf("findings %v", got)
	}
	all, _ := json.Marshal(body)
	for _, bad := range []string{st.Root, "storage_path", "/episodes/", "password", "token"} {
		if strings.Contains(string(all), bad) {
			t.Errorf("response contains %q", bad)
		}
	}
	// Approval stays bound to the exact hash.
	path := "/api/videos/" + strconv.FormatInt(v.ID, 10) + "/decision"
	if code, _ := call(t, ts, "POST", path, "", `{"decision":"approved","sha256":"`+strings.Repeat("0", 64)+`"}`); code != http.StatusConflict {
		t.Fatalf("approve with a wrong hash: %d, want 409", code)
	}
	if code, _ := call(t, ts, "POST", path, "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`); code != http.StatusCreated {
		t.Fatalf("approve with the exact hash: %d", code)
	}
}

func TestReviewOfAHistoryVideoWithChangedScriptFileShowsNoClaims(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("history-video-bytes-2"), "qa_passed")
	a := seedScriptFor(t, db, st, v, []byte(`{"title":"t"}`), map[string]any{"profile": "history_mystery"})
	p, _ := st.Abs(a.StoragePath)
	if err := os.WriteFile(p, []byte(`{"title":"changed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body := call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/review", "", "")
	h, _ := body["history"].(map[string]any)
	if h == nil || h["unreadable"] == "" || len(h["claims"].([]any)) != 0 || len(h["findings"].([]any)) != 0 {
		t.Fatalf("history block %v", h)
	}
}

func TestReviewOfAShortsOrFictionalVideoHasNoHistoryBlock(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("plain-video-bytes"), "qa_passed")
	seedScriptFor(t, db, st, v, []byte(`{"title":"t"}`), map[string]any{"profile": "knowledge_shorts"})
	_, body := call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/review", "", "")
	if _, has := body["history"]; has {
		t.Fatalf("a shorts review must not have a history block: %v", body["history"])
	}
	v2 := seedVideo(t, db, st, []byte("fiction-video-bytes-3"), "qa_passed")
	seedScriptFor(t, db, st, v2, []byte(`{"title":"t"}`), nil)
	_, body = call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v2.ID, 10)+"/review", "", "")
	if _, has := body["history"]; has {
		t.Fatal("a fictional review must not have a history block")
	}
}
