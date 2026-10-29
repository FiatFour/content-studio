package canon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/store/storetest"
)

func day(s string) *time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestPresent(t *testing.T) {
	alive := Character{Slug: "a", Name: "A", Status: StatusAlive}
	late := Character{Slug: "l", Name: "L", Status: StatusAlive, ArrivesOn: day("2030-01-01")}
	dead := Character{Slug: "d", Name: "D", Status: StatusDeceased, StatusSince: day("2030-06-01")}
	cases := []struct {
		name string
		c    Character
		at   *time.Time
		bad  string
	}{
		{"alive, no date", alive, nil, ""},
		{"before arrival", late, day("2029-12-31"), "not arrived"},
		{"on arrival day", late, day("2030-01-01"), ""},
		{"deceased before death", dead, day("2030-05-31"), ""},
		{"deceased on the day", dead, day("2030-06-01"), "deceased"},
		{"deceased, no date", dead, nil, "deceased"},
	}
	for _, tc := range cases {
		err := Present(tc.c, tc.at)
		if tc.bad == "" && err != nil || tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.bad)
		}
	}
}

func TestCheckSpeakersRejectsUnknownAndAbsent(t *testing.T) {
	chars := []Character{{Slug: "a", Name: "A", Status: StatusAlive}, {Slug: "d", Name: "D", Status: StatusMovedAway, StatusSince: day("2030-01-01")}}
	if err := CheckSpeakers([]string{"a", "a"}, chars, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckSpeakers([]string{"a", "zz"}, chars, nil); err == nil || !strings.Contains(err.Error(), `"zz"`) {
		t.Fatalf("unknown speaker: %v", err)
	}
	if err := CheckSpeakers([]string{"d"}, chars, day("2030-02-01")); err == nil || !strings.Contains(err.Error(), "moved away") {
		t.Fatalf("absent speaker: %v", err)
	}
	if err := CheckSpeakers([]string{"d"}, chars, day("2029-12-01")); err != nil {
		t.Fatalf("moved away later: %v", err)
	}
}

func TestPromptBlockOnlyHoldsWhatItIsGiven(t *testing.T) {
	out := PromptBlock([]Character{{Slug: "a", Name: "A", Appearance: "old woman", Voice: "warm"}},
		[]Summary{{Number: 2, Title: "T", Summary: "S", OpenThreads: []string{"x"}}})
	for _, want := range []string{`id "a"`, "old woman", `Episode 2 "T": S`, "Open threads: x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestStoreQueries(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, eid := storetest.Episode(t, db)
	if _, err := db.Exec(ctx, `INSERT INTO characters (project_id, slug, name, appearance, voice) VALUES ($1, 'b', 'B', 'old man here', 'gruff voice')`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO episode_summaries (episode_id, summary) VALUES ($1, 'it happened')`, eid); err != nil {
		t.Fatal(err)
	}
	chars, err := Characters(ctx, db, pid)
	if err != nil || len(chars) != 1 || chars[0].Slug != "b" || chars[0].Status != StatusAlive {
		t.Fatalf("characters: %v %+v", err, chars)
	}
	if s, err := RecentSummaries(ctx, db, pid, 1, 3); err != nil || len(s) != 0 {
		t.Fatalf("earlier summaries of episode 1 must be empty: %v %+v", err, s)
	}
	if s, err := RecentSummaries(ctx, db, pid, 2, 3); err != nil || len(s) != 1 || s[0].Summary != "it happened" {
		t.Fatalf("summaries: %v %+v", err, s)
	}
	if _, err := db.Exec(ctx, `INSERT INTO characters (project_id, slug, name, appearance, voice, status) VALUES ($1, 'c', 'C', 'someone here', 'some voice', 'deceased')`, pid); err == nil {
		t.Fatal("deceased without status_since must be rejected by the database")
	}
}
