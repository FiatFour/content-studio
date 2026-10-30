package canon

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/store/storetest"
)

func d(s string) *time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func pending(kind, payload string) Proposal {
	return Proposal{ID: 1, Kind: kind, Payload: json.RawMessage(payload), Status: StatusProposed}
}

func has(r Report, code, sev string) bool {
	for _, f := range r.Findings {
		if f.Code == code && f.Severity == sev {
			return true
		}
	}
	return false
}

func chars(cs ...Character) []Character { return cs }

func alive(slug string) Character { return Character{Slug: slug, Name: slug, Status: StatusAlive} }

func TestAnalyze(t *testing.T) {
	ann, bob, cy := alive("ann"), alive("bob"), alive("cy")
	deadBob := Character{Slug: "bob", Name: "bob", Status: StatusDeceased, StatusSince: d("2020-01-01")}
	lateCy := Character{Slug: "cy", Name: "cy", Status: StatusAlive, ArrivesOn: d("2030-01-01")}
	eps := []EpisodeInfo{
		{ID: 10, Number: 1, Title: "one", InStoryDate: d("2024-05-01"), HasSummary: true, CharactersPresent: []string{"ann", "bob"}},
		{ID: 11, Number: 2, Title: "two", InStoryDate: d("2024-04-01"), HasSummary: true, CharactersPresent: []string{"ann"}},
		{ID: 12, Number: 3, Title: "three", HasSummary: false},
	}
	rel := func(typ, f, to string, s, e *time.Time) Rel {
		return Rel{ID: 1, From: f, To: to, Type: typ, StartsOn: s, EndsOn: e}
	}

	cases := []struct {
		name     string
		p        Proposal
		s        Snapshot
		want     [][2]string // code, severity that must appear
		wantNone []string    // codes that must not appear
	}{
		{"parent cycle direct", pending(KindRelationshipAdd, `{"from":"bob","to":"ann","type":"parent"}`),
			Snapshot{Characters: chars(ann, bob), Relationships: []Rel{rel("parent", "ann", "bob", nil, nil)}},
			[][2]string{{"relationship.ancestor_cycle", SeverityConflict}}, nil},
		{"grandparent cycle via child type", pending(KindRelationshipAdd, `{"from":"ann","to":"cy","type":"child"}`),
			Snapshot{Characters: chars(ann, bob, cy), Relationships: []Rel{rel("parent", "ann", "bob", nil, nil), rel("parent", "bob", "cy", nil, nil)}},
			[][2]string{{"relationship.ancestor_cycle", SeverityConflict}}, nil}, // ann child of cy, yet ann is cy's grandparent
		{"no cycle", pending(KindRelationshipAdd, `{"from":"ann","to":"cy","type":"parent"}`),
			Snapshot{Characters: chars(ann, bob, cy), Relationships: []Rel{rel("parent", "ann", "bob", nil, nil)}},
			nil, []string{"relationship.ancestor_cycle"}},
		{"self relationship", pending(KindRelationshipAdd, `{"from":"ann","to":"ann","type":"friend"}`),
			Snapshot{Characters: chars(ann)}, [][2]string{{"relationship.self", SeverityConflict}}, nil},
		{"end before start", pending(KindRelationshipAdd, `{"from":"ann","to":"bob","type":"friend","starts_on":"2024-05-01","ends_on":"2024-01-01"}`),
			Snapshot{Characters: chars(ann, bob)}, [][2]string{{"chronology.relationship_end_before_start", SeverityConflict}}, nil},
		{"spouse overlap", pending(KindRelationshipAdd, `{"from":"ann","to":"cy","type":"spouse","starts_on":"2022-01-01"}`),
			Snapshot{Characters: chars(ann, bob, cy), Relationships: []Rel{rel("spouse", "ann", "bob", d("2020-01-01"), nil)}},
			[][2]string{{"relationship.spouse_overlap", SeverityWarning}}, nil},
		{"spouse no overlap after end", pending(KindRelationshipAdd, `{"from":"ann","to":"cy","type":"spouse","starts_on":"2022-01-01"}`),
			Snapshot{Characters: chars(ann, bob, cy), Relationships: []Rel{rel("spouse", "ann", "bob", d("2020-01-01"), d("2021-01-01"))}},
			nil, []string{"relationship.spouse_overlap"}},
		{"spouses never coexist", pending(KindRelationshipAdd, `{"from":"bob","to":"cy","type":"spouse"}`),
			Snapshot{Characters: chars(deadBob, lateCy)}, [][2]string{{"relationship.spouses_not_contemporary", SeverityConflict}}, nil},
		{"duplicate reversed symmetric", pending(KindRelationshipAdd, `{"from":"bob","to":"ann","type":"friend"}`),
			Snapshot{Characters: chars(ann, bob), Relationships: []Rel{rel("friend", "ann", "bob", nil, nil)}},
			[][2]string{{"relationship.duplicate", SeverityWarning}}, nil},
		{"duplicate parent vs child", pending(KindRelationshipAdd, `{"from":"bob","to":"ann","type":"child"}`),
			Snapshot{Characters: chars(ann, bob), Relationships: []Rel{rel("parent", "ann", "bob", nil, nil)}},
			[][2]string{{"relationship.duplicate", SeverityWarning}}, nil},
		{"relationship after departure", pending(KindRelationshipAdd, `{"from":"ann","to":"bob","type":"friend","starts_on":"2021-01-01"}`),
			Snapshot{Characters: chars(ann, deadBob)}, [][2]string{{"chronology.relationship_after_departure", SeverityWarning}}, nil},
		{"missing character", pending(KindRelationshipAdd, `{"from":"ann","to":"zed","type":"friend"}`),
			Snapshot{Characters: chars(ann)}, [][2]string{{"character.missing", SeverityConflict}}, nil},
		{"update kills character present later", pending(KindCharacterUpdate, `{"slug":"bob","status":"deceased","status_since":"2024-01-01"}`),
			Snapshot{Characters: chars(bob), Episodes: eps}, [][2]string{{"presence.episode_conflict", SeverityConflict}, {"age.no_birth_data", SeverityInfo}}, nil},
		{"update arrival after episode", pending(KindCharacterUpdate, `{"slug":"ann","arrives_on":"2024-04-15"}`),
			Snapshot{Characters: chars(ann), Episodes: eps}, [][2]string{{"presence.episode_conflict", SeverityConflict}}, nil},
		{"update fine", pending(KindCharacterUpdate, `{"slug":"ann","personality":"calm"}`),
			Snapshot{Characters: chars(ann), Episodes: eps}, nil, []string{"presence.episode_conflict"}},
		{"update status before arrival", pending(KindCharacterUpdate, `{"slug":"ann","status":"moved_away","status_since":"2000-01-01"}`),
			Snapshot{Characters: chars(Character{Slug: "ann", Name: "ann", Status: StatusAlive, ArrivesOn: d("2010-01-01")})},
			[][2]string{{"chronology.status_before_arrival", SeverityConflict}, {"presence.no_summaries", SeverityInfo}}, nil},
		{"update missing", pending(KindCharacterUpdate, `{"slug":"zed","voice":"deep voice"}`),
			Snapshot{}, [][2]string{{"character.missing", SeverityConflict}}, nil},
		{"update shifts existing relationship", pending(KindCharacterUpdate, `{"slug":"bob","status":"deceased","status_since":"2020-01-01"}`),
			Snapshot{Characters: chars(ann, bob), Relationships: []Rel{rel("friend", "ann", "bob", d("2021-01-01"), nil)}},
			[][2]string{{"chronology.relationship_after_departure", SeverityWarning}}, nil},
		{"create slug exists", pending(KindCharacterCreate, `{"slug":"ann","name":"Other","appearance":"short black hair","voice":"bright voice"}`),
			Snapshot{Characters: chars(ann)}, [][2]string{{"character.slug_exists", SeverityConflict}}, nil},
		{"create name duplicate", pending(KindCharacterCreate, `{"slug":"ann2","name":"ANN","appearance":"short black hair","voice":"bright voice"}`),
			Snapshot{Characters: chars(ann)}, [][2]string{{"character.name_duplicate", SeverityWarning}}, nil},
		{"fact duplicate normalized", pending(KindFactAdd, `{"statement":"Ann owns a bike!"}`),
			Snapshot{Facts: []Fact{{ID: 7, Statement: "ann  owns a BIKE."}}},
			[][2]string{{"fact.duplicate", SeverityConflict}, {"fact.free_text", SeverityInfo}}, nil},
		{"fact source missing", pending(KindFactAdd, `{"statement":"Ann owns a bike","source_episode_id":99}`),
			Snapshot{Episodes: eps}, [][2]string{{"fact.source_missing", SeverityConflict}}, nil},
		{"fact source out of order", pending(KindFactAdd, `{"statement":"Ann owns a bike","source_episode_id":11}`),
			Snapshot{Episodes: eps}, [][2]string{{"chronology.episode_order", SeverityWarning}}, []string{"fact.duplicate"}},
		{"fact source undated", pending(KindFactAdd, `{"statement":"Ann owns a bike","source_episode_id":12}`),
			Snapshot{Episodes: eps}, [][2]string{{"chronology.episode_no_date", SeverityInfo}}, nil},
		{"invalid stored payload", pending(KindFactAdd, `{"nope":1}`), Snapshot{}, [][2]string{{"payload.invalid", SeverityConflict}}, nil},
		{"decided proposal not checked", Proposal{ID: 1, Kind: KindFactAdd, Payload: json.RawMessage(`{"statement":"Ann owns a bike"}`), Status: StatusApproved},
			Snapshot{Facts: []Fact{{ID: 1, Statement: "Ann owns a bike"}}}, [][2]string{{"proposal.decided", SeverityInfo}}, []string{"fact.duplicate"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Analyze(c.p, c.s)
			if !r.Advisory {
				t.Fatal("report must be advisory")
			}
			for _, w := range c.want {
				if !has(r, w[0], w[1]) {
					t.Errorf("missing %v in %+v", w, r.Findings)
				}
			}
			for _, code := range c.wantNone {
				for _, f := range r.Findings {
					if f.Code == code {
						t.Errorf("unexpected %s: %s", code, f.Message)
					}
				}
			}
			if again := Analyze(c.p, c.s); !reflect.DeepEqual(r, again) {
				t.Error("analysis is not deterministic")
			}
			rank := -1
			for _, f := range r.Findings {
				if severityRank[f.Severity] < rank {
					t.Errorf("findings not ordered by severity: %+v", r.Findings)
				}
				rank = severityRank[f.Severity]
			}
		})
	}
}

func TestAnalyzeCannotCheckNeverSilent(t *testing.T) {
	// No summaries and no birth data: the character check must say so rather than pass.
	r := Analyze(pending(KindCharacterUpdate, `{"slug":"ann","status":"deceased","status_since":"2024-01-01"}`), Snapshot{Characters: chars(alive("ann"))})
	if !has(r, "age.no_birth_data", SeverityInfo) || !has(r, "presence.no_summaries", SeverityInfo) {
		t.Fatalf("%+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Severity == SeverityInfo && !strings.HasPrefix(f.Message, "cannot check:") {
			t.Errorf("info without 'cannot check:' prefix: %q", f.Message)
		}
	}
}

func TestAnalyzeImpact(t *testing.T) {
	s := Snapshot{
		Characters:    chars(alive("ann"), alive("bob")),
		Relationships: []Rel{{ID: 5, From: "ann", To: "bob", Type: "friend"}},
		Episodes: []EpisodeInfo{
			{ID: 10, Number: 2, Title: "two", InStoryDate: d("2024-05-01"), HasSummary: true, CharactersPresent: []string{"bob"}},
			{ID: 11, Number: 1, Title: "one", HasSummary: true, CharactersPresent: []string{"zed"}},
		},
	}
	r := Analyze(pending(KindCharacterUpdate, `{"slug":"bob","voice":"deep voice"}`), s)
	if len(r.Impact.Characters) != 1 || r.Impact.Characters[0].Slug != "bob" || !r.Impact.Characters[0].Exists {
		t.Fatalf("characters: %+v", r.Impact.Characters)
	}
	if len(r.Impact.Relationships) != 1 || r.Impact.Relationships[0].ID != 5 {
		t.Fatalf("relationships: %+v", r.Impact.Relationships)
	}
	if len(r.Impact.Episodes) != 1 || r.Impact.Episodes[0].Number != 2 || r.Impact.Episodes[0].Reason != "present" {
		t.Fatalf("episodes: %+v", r.Impact.Episodes)
	}
}

func TestCheckProposalIsReadOnlyAndDoesNotDecide(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	pid, _ := storetest.Episode(t, db)
	id := propose(t, db, pid, KindFactAdd, `{"statement":"Mia has a yellow bike"}`)
	before := count(t, db, `SELECT count(*) FROM audit_log`)
	r, err := CheckProposal(ctx, db, pid, id)
	if err != nil || r.Status != StatusProposed || !r.Advisory {
		t.Fatalf("%v %+v", err, r)
	}
	if count(t, db, `SELECT count(*) FROM canon_proposal_decisions`) != 0 || count(t, db, `SELECT count(*) FROM audit_log`) != before ||
		count(t, db, `SELECT count(*) FROM established_facts`) != 0 {
		t.Fatal("checks wrote something")
	}
	if err := Approve(ctx, db, pid, id, owner, "fine"); err != nil {
		t.Fatal(err)
	}
	// A proposal flagged as a conflict can still be approved: checks are advisory and do not gate Approve.
	dup := propose(t, db, pid, KindFactAdd, `{"statement":"mia has a yellow bike."}`)
	r, err = CheckProposal(ctx, db, pid, dup)
	if err != nil || !has(r, "fact.duplicate", SeverityConflict) {
		t.Fatalf("%v %+v", err, r.Findings)
	}
	if err := Approve(ctx, db, pid, dup, owner, "owner decides despite the conflict finding"); err != nil {
		t.Fatalf("approve must not depend on the checks: %v", err)
	}
	if _, err := CheckProposal(ctx, db, pid, 999999); err == nil {
		t.Fatal("missing proposal must error")
	}
}
