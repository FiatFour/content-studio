package canon

// Deterministic impact and conflict checks for a canon proposal (M3, canon.md "Deterministic checks").
// Pure code, no model: Analyze is a pure function of a Snapshot of current canon, and CheckProposal only
// reads. The report is ADVISORY display for the owner. Nothing here approves, rejects, blocks or decides
// a proposal, and Approve does not consult it. Where the data a check needs does not exist, the report
// says "cannot check: <why>" as an info finding instead of passing silently.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SeverityConflict = "conflict"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Finding is one result of a check. Code is stable and machine-readable (the UI maps it to TH/EN);
// Message is plain English for logs and as a fallback.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Rel is an established relationship with character slugs.
type Rel struct {
	ID       int64
	From, To string
	Type     string
	StartsOn *time.Time
	EndsOn   *time.Time
}

// Fact is an established fact (free-text statement tied to an optional source episode).
type Fact struct {
	ID              int64
	Statement       string
	SourceEpisodeID *int64
}

// EpisodeInfo is an episode with what is known about it. HasSummary is false when the owner has not
// written a summary, in which case CharactersPresent is unknown rather than empty.
type EpisodeInfo struct {
	ID                int64
	Number            int
	Title             string
	InStoryDate       *time.Time
	HasSummary        bool
	CharactersPresent []string
}

// Snapshot is the current canon the checks compare a proposal against.
type Snapshot struct {
	Characters    []Character
	Relationships []Rel
	Facts         []Fact
	Episodes      []EpisodeInfo
}

type AffectedCharacter struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Exists bool   `json:"exists"` // false when the proposal names a slug that is not canon (yet)
}

type AffectedRelationship struct {
	ID   int64  `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

type AffectedEpisode struct {
	ID          int64   `json:"id"`
	Number      int     `json:"number"`
	Title       string  `json:"title"`
	InStoryDate *string `json:"in_story_date"`
	Reason      string  `json:"reason"` // "present" (summary lists an affected character) or "source"
}

// Impact is what the proposal touches in current canon.
type Impact struct {
	Characters    []AffectedCharacter    `json:"characters"`
	Relationships []AffectedRelationship `json:"relationships"`
	Episodes      []AffectedEpisode      `json:"episodes"`
}

// Report is the advisory result for one proposal.
type Report struct {
	ProposalID int64     `json:"proposal_id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Advisory   bool      `json:"advisory"` // always true: the report never decides anything
	Impact     Impact    `json:"impact"`
	Findings   []Finding `json:"findings"`
}

func fnd(sev, code, format string, a ...any) Finding {
	return Finding{Code: code, Severity: sev, Message: fmt.Sprintf(format, a...)}
}

func cannot(code, why string) Finding {
	return fnd(SeverityInfo, code, "cannot check: %s", why)
}

// Analyze checks proposal p against s. It has no side effects and does not mutate s.
func Analyze(p Proposal, s Snapshot) Report {
	r := Report{ProposalID: p.ID, Kind: p.Kind, Status: p.Status, Advisory: true,
		Impact:   Impact{Characters: []AffectedCharacter{}, Relationships: []AffectedRelationship{}, Episodes: []AffectedEpisode{}},
		Findings: []Finding{}}
	if p.Status != StatusProposed {
		// A decided proposal is already part of canon (or rejected); comparing it with itself would
		// only produce false duplicates.
		r.Findings = append(r.Findings, fnd(SeverityInfo, "proposal.decided", "this proposal is already %s; checks run on pending proposals only", p.Status))
		return r
	}
	a := analysis{s: s, rep: &r, bySlug: map[string]Character{}, chronoDone: map[int64]bool{}}
	for _, c := range s.Characters {
		a.bySlug[c.Slug] = c
	}
	switch p.Kind {
	case KindCharacterCreate:
		var pl CharacterCreatePayload
		if a.decode(p, &pl) {
			a.characterCreate(pl)
		}
	case KindCharacterUpdate:
		var pl CharacterUpdatePayload
		if a.decode(p, &pl) {
			a.characterUpdate(pl)
		}
	case KindRelationshipAdd:
		var pl RelationshipAddPayload
		if a.decode(p, &pl) {
			a.relationshipAdd(pl)
		}
	case KindFactAdd:
		var pl FactAddPayload
		if a.decode(p, &pl) {
			a.factAdd(pl)
		}
	default:
		a.add(fnd(SeverityConflict, "proposal.unknown_kind", "unknown proposal kind %q", p.Kind))
	}
	if p.EpisodeID != nil {
		a.chronology(*p.EpisodeID)
	}
	a.episodesFor(p)
	sortFindings(r.Findings)
	return r
}

type analysis struct {
	s      Snapshot
	rep    *Report
	bySlug map[string]Character
	// chronoDone records episodes whose order was already checked.
	chronoDone map[int64]bool
}

func (a *analysis) add(f Finding) { a.rep.Findings = append(a.rep.Findings, f) }

func (a *analysis) decode(p Proposal, v any) bool {
	if err := decodeStrict(p.Payload, v); err != nil {
		a.add(fnd(SeverityConflict, "payload.invalid", "the stored payload does not match the schema for %s", p.Kind))
		return false
	}
	return true
}

func (a *analysis) affect(slug string) {
	for _, c := range a.rep.Impact.Characters {
		if c.Slug == slug {
			return
		}
	}
	ch, ok := a.bySlug[slug]
	a.rep.Impact.Characters = append(a.rep.Impact.Characters, AffectedCharacter{Slug: slug, Name: ch.Name, Exists: ok})
}

func (a *analysis) existing(role, slug string) (Character, bool) {
	c, ok := a.bySlug[slug]
	if !ok {
		a.add(fnd(SeverityConflict, "character.missing", "%s %q does not exist in canon, so approving would fail", role, slug))
	}
	return c, ok
}

func parseDate(v string) *time.Time {
	d, _ := date("", v)
	return d
}

func fmtDate(d *time.Time) string {
	if d == nil {
		return "unbounded"
	}
	return d.Format(time.DateOnly)
}

func orAlive(s string) string {
	if s == "" {
		return StatusAlive
	}
	return s
}

func (a *analysis) characterCreate(pl CharacterCreatePayload) {
	a.affect(pl.Slug)
	if _, dup := a.bySlug[pl.Slug]; dup {
		a.add(fnd(SeverityConflict, "character.slug_exists", "a character with id %q already exists, so approving would fail", pl.Slug))
	}
	for _, c := range a.s.Characters {
		if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(pl.Name)) && c.Slug != pl.Slug {
			a.add(fnd(SeverityWarning, "character.name_duplicate", "a character named %q already exists (id %q)", c.Name, c.Slug))
		}
	}
	a.characterState(Character{Slug: pl.Slug, Name: pl.Name, ArrivesOn: parseDate(pl.ArrivesOn),
		Status: orAlive(pl.Status), StatusSince: parseDate(pl.StatusSince)})
}

func (a *analysis) characterUpdate(pl CharacterUpdatePayload) {
	a.affect(pl.Slug)
	c, ok := a.existing("character", pl.Slug)
	if !ok {
		return
	}
	if pl.Name != nil {
		c.Name = *pl.Name
	}
	if pl.ArrivesOn != nil {
		c.ArrivesOn = parseDate(*pl.ArrivesOn)
	}
	if pl.Status != nil {
		c.Status = *pl.Status
	}
	if pl.StatusSince != nil {
		c.StatusSince = parseDate(*pl.StatusSince)
	}
	if c.Status == StatusAlive { // as the commit path: an alive character keeps no status_since
		c.StatusSince = nil
	}
	a.characterState(c)
	// Existing relationships must still fit the changed lifetime.
	for _, rel := range a.s.Relationships {
		if rel.From == c.Slug || rel.To == c.Slug {
			a.relBounds(rel, a.lifeOf(rel.From, &c), a.lifeOf(rel.To, &c), "existing relationship")
		}
	}
}

// characterState checks a (merged) character against its own dates and the episodes it appears in.
func (a *analysis) characterState(c Character) {
	if c.Status != StatusAlive && c.StatusSince != nil && c.ArrivesOn != nil && c.StatusSince.Before(*c.ArrivesOn) {
		a.add(fnd(SeverityConflict, "chronology.status_before_arrival", "%s (%s) would become %s on %s, before arriving on %s",
			c.Name, c.Slug, strings.ReplaceAll(c.Status, "_", " "), fmtDate(c.StatusSince), fmtDate(c.ArrivesOn)))
	}
	// Age and life stage: the data model has neither a birth date nor a life stage.
	a.add(cannot("age.no_birth_data", "characters have no birth date or life stage recorded, so age cannot be compared with episode in-story dates"))

	datedPresent, summaries := 0, 0
	for _, e := range a.s.Episodes {
		if !e.HasSummary {
			continue
		}
		summaries++
		if !contains(e.CharactersPresent, c.Slug) || e.InStoryDate == nil {
			continue
		}
		datedPresent++
		if err := Present(c, e.InStoryDate); err != nil {
			a.add(fnd(SeverityConflict, "presence.episode_conflict", "episode %d (%s, in-story %s) lists %s but the change makes this wrong: %v",
				e.Number, e.Title, e.InStoryDate.Format(time.DateOnly), c.Slug, err))
		}
	}
	if summaries == 0 {
		a.add(cannot("presence.no_summaries", "no episode has a summary with characters present, so appearances cannot be compared"))
	} else if datedPresent == 0 {
		a.add(cannot("presence.no_dated_appearances", fmt.Sprintf("no episode with a date and a summary lists %q as present", c.Slug)))
	}
}

// life is a character's existence interval: nil bounds are open.
type life struct{ from, to *time.Time }

func (a *analysis) lifeOf(slug string, override *Character) life {
	c, ok := a.bySlug[slug]
	if override != nil && override.Slug == slug {
		c, ok = *override, true
	}
	if !ok {
		return life{}
	}
	l := life{from: c.ArrivesOn}
	if c.Status != StatusAlive {
		l.to = c.StatusSince
	}
	return l
}

// overlap reports whether two closed intervals intersect; nil bounds are open.
func overlap(as, ae, bs, be *time.Time) bool {
	if ae != nil && bs != nil && ae.Before(*bs) {
		return false
	}
	if be != nil && as != nil && be.Before(*as) {
		return false
	}
	return true
}

// relBounds checks a relationship's period against its own order and both characters' lifetimes.
func (a *analysis) relBounds(r Rel, from, to life, what string) {
	if r.StartsOn != nil && r.EndsOn != nil && r.EndsOn.Before(*r.StartsOn) {
		a.add(fnd(SeverityConflict, "chronology.relationship_end_before_start", "%s %s %s->%s ends on %s, before it starts on %s", what, r.Type, r.From, r.To, fmtDate(r.EndsOn), fmtDate(r.StartsOn)))
	}
	for _, x := range []struct {
		slug string
		l    life
	}{{r.From, from}, {r.To, to}} {
		if r.StartsOn != nil && x.l.from != nil && r.StartsOn.Before(*x.l.from) {
			a.add(fnd(SeverityWarning, "chronology.relationship_before_arrival", "%s %s %s->%s starts on %s, before %s arrives on %s",
				what, r.Type, r.From, r.To, fmtDate(r.StartsOn), x.slug, fmtDate(x.l.from)))
		}
		if r.StartsOn != nil && x.l.to != nil && r.StartsOn.After(*x.l.to) {
			a.add(fnd(SeverityWarning, "chronology.relationship_after_departure", "%s %s %s->%s starts on %s, after %s is gone (%s)",
				what, r.Type, r.From, r.To, fmtDate(r.StartsOn), x.slug, fmtDate(x.l.to)))
		}
	}
}

var symmetric = map[string]bool{"sibling": true, "spouse": true, "partner": true, "friend": true, "rival": true, "coworker": true, "neighbor": true}

// canonRel puts a relationship in one canonical direction so duplicates compare equal: child(a,b) is
// parent(b,a), and symmetric types order their ends.
func canonRel(typ, from, to string) (string, string, string) {
	if typ == "child" {
		typ, from, to = "parent", to, from
	}
	if symmetric[typ] && from > to {
		from, to = to, from
	}
	return typ, from, to
}

func (a *analysis) relationshipAdd(pl RelationshipAddPayload) {
	a.affect(pl.From)
	a.affect(pl.To)
	n := Rel{From: pl.From, To: pl.To, Type: pl.Type, StartsOn: parseDate(pl.StartsOn), EndsOn: parseDate(pl.EndsOn)}
	if pl.From == pl.To {
		a.add(fnd(SeverityConflict, "relationship.self", "%q cannot have a relationship with itself", pl.From))
		return
	}
	_, okFrom := a.existing("character", pl.From)
	_, okTo := a.existing("character", pl.To)
	for _, rel := range a.s.Relationships {
		if rel.From == pl.From || rel.To == pl.From || rel.From == pl.To || rel.To == pl.To {
			a.rep.Impact.Relationships = append(a.rep.Impact.Relationships, AffectedRelationship{ID: rel.ID, From: rel.From, To: rel.To, Type: rel.Type})
		}
	}
	a.relBounds(n, a.lifeOf(pl.From, nil), a.lifeOf(pl.To, nil), "relationship")

	nt, nf, nto := canonRel(n.Type, n.From, n.To)
	for _, rel := range a.s.Relationships {
		et, ef, eto := canonRel(rel.Type, rel.From, rel.To)
		if et == nt && ef == nf && eto == nto && overlap(n.StartsOn, n.EndsOn, rel.StartsOn, rel.EndsOn) {
			a.add(fnd(SeverityWarning, "relationship.duplicate", "an established %s relationship between %s and %s already covers this period", rel.Type, rel.From, rel.To))
		}
	}
	if nt == "parent" {
		a.ancestry(nf, nto)
	}
	if n.Type == "spouse" {
		a.spouse(n, okFrom && okTo)
	}
	a.add(cannot("relationship.age_gap", "characters have no birth date, so parent and child ages cannot be compared"))
}

// ancestry reports when making parent a parent of child closes a loop (a character becomes their own
// ancestor). Edges are parent->child from established parent and child relationships; dates are ignored.
func (a *analysis) ancestry(parent, child string) {
	kids := map[string][]string{}
	for _, rel := range a.s.Relationships {
		t, f, to := canonRel(rel.Type, rel.From, rel.To)
		if t == "parent" {
			kids[f] = append(kids[f], to)
		}
	}
	// A cycle appears if child already reaches parent through descendants (or parent == child).
	seen := map[string]bool{}
	var reach func(x string) bool
	reach = func(x string) bool {
		if x == parent {
			return true
		}
		if seen[x] {
			return false
		}
		seen[x] = true
		for _, k := range kids[x] {
			if reach(k) {
				return true
			}
		}
		return false
	}
	if reach(child) {
		a.add(fnd(SeverityConflict, "relationship.ancestor_cycle", "making %s a parent of %s would make a character their own ancestor", parent, child))
	}
}

func (a *analysis) spouse(n Rel, bothExist bool) {
	for _, rel := range a.s.Relationships {
		if rel.Type != "spouse" {
			continue
		}
		shared := rel.From == n.From || rel.To == n.From || rel.From == n.To || rel.To == n.To
		_, nf, nt := canonRel("spouse", n.From, n.To)
		_, ef, et := canonRel("spouse", rel.From, rel.To)
		if shared && !(nf == ef && nt == et) && overlap(n.StartsOn, n.EndsOn, rel.StartsOn, rel.EndsOn) {
			a.add(fnd(SeverityWarning, "relationship.spouse_overlap", "spouse periods overlap: %s and %s are already married (%s to %s)",
				rel.From, rel.To, fmtDate(rel.StartsOn), fmtDate(rel.EndsOn)))
		}
	}
	if !bothExist {
		return
	}
	f, t := a.lifeOf(n.From, nil), a.lifeOf(n.To, nil)
	if !overlap(f.from, f.to, t.from, t.to) {
		a.add(fnd(SeverityConflict, "relationship.spouses_not_contemporary", "%s (%s to %s) and %s (%s to %s) never exist at the same time",
			n.From, fmtDate(f.from), fmtDate(f.to), n.To, fmtDate(t.from), fmtDate(t.to)))
	}
}

func normStatement(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, ".!?;, ")
}

func (a *analysis) factAdd(pl FactAddPayload) {
	n := normStatement(pl.Statement)
	for _, f := range a.s.Facts {
		if normStatement(f.Statement) == n {
			a.add(fnd(SeverityConflict, "fact.duplicate", "this fact repeats established fact %d", f.ID))
		}
	}
	a.add(cannot("fact.free_text", "established facts are free text with no structured fields, so contradictions with them cannot be detected in code"))
	if pl.SourceEpisodeID != 0 {
		found := false
		for _, e := range a.s.Episodes {
			found = found || e.ID == pl.SourceEpisodeID
		}
		if !found {
			a.add(fnd(SeverityConflict, "fact.source_missing", "the source episode does not exist in this series, so approving would fail"))
		} else {
			a.chronology(pl.SourceEpisodeID)
		}
	}
}

// chronology checks that the episode's in-story date keeps story order by episode number.
func (a *analysis) chronology(episodeID int64) {
	var x *EpisodeInfo
	for i := range a.s.Episodes {
		if a.s.Episodes[i].ID == episodeID {
			x = &a.s.Episodes[i]
		}
	}
	if x == nil {
		return
	}
	if a.chronoDone[episodeID] {
		return // the proposal's episode can also be a fact's source
	}
	a.chronoDone[episodeID] = true
	if x.InStoryDate == nil {
		a.add(cannot("chronology.episode_no_date", fmt.Sprintf("episode %d has no in-story date, so its order in the timeline cannot be checked", x.Number)))
		return
	}
	var prev, next *EpisodeInfo
	for i := range a.s.Episodes {
		e := &a.s.Episodes[i]
		if e.InStoryDate == nil || e.ID == x.ID {
			continue
		}
		if e.Number < x.Number && e.InStoryDate.After(*x.InStoryDate) && (prev == nil || e.Number > prev.Number) {
			prev = e
		}
		if e.Number > x.Number && e.InStoryDate.Before(*x.InStoryDate) && (next == nil || e.Number < next.Number) {
			next = e
		}
	}
	if prev != nil {
		a.add(fnd(SeverityWarning, "chronology.episode_order", "episode %d (%s) is dated before the earlier episode %d (%s)", x.Number, fmtDate(x.InStoryDate), prev.Number, fmtDate(prev.InStoryDate)))
	}
	if next != nil {
		a.add(fnd(SeverityWarning, "chronology.episode_order", "episode %d (%s) is dated after the later episode %d (%s)", x.Number, fmtDate(x.InStoryDate), next.Number, fmtDate(next.InStoryDate)))
	}
}

// episodesFor lists the episodes the proposal touches: those whose summary lists an affected
// character, plus the proposal's own and a fact's source episode. For an update it also lists the
// affected characters' existing relationships.
func (a *analysis) episodesFor(p Proposal) {
	src := map[int64]bool{}
	if p.EpisodeID != nil {
		src[*p.EpisodeID] = true
	}
	if p.Kind == KindFactAdd {
		var pl FactAddPayload
		if decodeStrict(p.Payload, &pl) == nil && pl.SourceEpisodeID != 0 {
			src[pl.SourceEpisodeID] = true
		}
	}
	for _, e := range a.s.Episodes {
		reason := ""
		if src[e.ID] {
			reason = "source"
		} else if e.HasSummary {
			for _, c := range a.rep.Impact.Characters {
				if contains(e.CharactersPresent, c.Slug) {
					reason = "present"
				}
			}
		}
		if reason == "" {
			continue
		}
		ae := AffectedEpisode{ID: e.ID, Number: e.Number, Title: e.Title, Reason: reason}
		if e.InStoryDate != nil {
			d := e.InStoryDate.Format(time.DateOnly)
			ae.InStoryDate = &d
		}
		a.rep.Impact.Episodes = append(a.rep.Impact.Episodes, ae)
	}
	if p.Kind == KindCharacterUpdate {
		for _, rel := range a.s.Relationships {
			for _, c := range a.rep.Impact.Characters {
				if rel.From == c.Slug || rel.To == c.Slug {
					a.rep.Impact.Relationships = append(a.rep.Impact.Relationships, AffectedRelationship{ID: rel.ID, From: rel.From, To: rel.To, Type: rel.Type})
					break
				}
			}
		}
	}
	im := &a.rep.Impact
	sort.Slice(im.Characters, func(i, j int) bool { return im.Characters[i].Slug < im.Characters[j].Slug })
	sort.Slice(im.Relationships, func(i, j int) bool { return im.Relationships[i].ID < im.Relationships[j].ID })
	sort.Slice(im.Episodes, func(i, j int) bool { return im.Episodes[i].Number < im.Episodes[j].Number })
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

var severityRank = map[string]int{SeverityConflict: 0, SeverityWarning: 1, SeverityInfo: 2}

// sortFindings gives a stable order: severity, then code, then message.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if severityRank[fs[i].Severity] != severityRank[fs[j].Severity] {
			return severityRank[fs[i].Severity] < severityRank[fs[j].Severity]
		}
		if fs[i].Code != fs[j].Code {
			return fs[i].Code < fs[j].Code
		}
		return fs[i].Message < fs[j].Message
	})
}

// LoadSnapshot reads current canon for the project. Read-only.
func LoadSnapshot(ctx context.Context, db *pgxpool.Pool, projectID int64) (Snapshot, error) {
	var s Snapshot
	var err error
	if s.Characters, err = Characters(ctx, db, projectID); err != nil {
		return s, err
	}
	rows, err := db.Query(ctx, `SELECT r.id, f.slug, t.slug, r.rel_type, r.starts_on, r.ends_on
		FROM character_relationships r JOIN characters f ON f.id = r.from_character JOIN characters t ON t.id = r.to_character
		WHERE r.project_id = $1 ORDER BY r.id`, projectID)
	if err != nil {
		return s, err
	}
	if s.Relationships, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Rel, error) {
		var x Rel
		return x, r.Scan(&x.ID, &x.From, &x.To, &x.Type, &x.StartsOn, &x.EndsOn)
	}); err != nil {
		return s, err
	}
	rows, err = db.Query(ctx, `SELECT id, statement, source_episode_id FROM established_facts WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return s, err
	}
	if s.Facts, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Fact, error) {
		var x Fact
		return x, r.Scan(&x.ID, &x.Statement, &x.SourceEpisodeID)
	}); err != nil {
		return s, err
	}
	rows, err = db.Query(ctx, `SELECT e.id, e.number, e.title, e.in_story_date, s.episode_id IS NOT NULL, COALESCE(s.characters_present, '{}')
		FROM episodes e LEFT JOIN episode_summaries s ON s.episode_id = e.id WHERE e.project_id = $1 ORDER BY e.number`, projectID)
	if err != nil {
		return s, err
	}
	s.Episodes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (EpisodeInfo, error) {
		var x EpisodeInfo
		return x, r.Scan(&x.ID, &x.Number, &x.Title, &x.InStoryDate, &x.HasSummary, &x.CharactersPresent)
	})
	return s, err
}

// CheckProposal loads proposal id and current canon and returns the advisory report. It only reads: it
// never writes canon and never records a decision. pgx.ErrNoRows when the proposal does not exist.
func CheckProposal(ctx context.Context, db *pgxpool.Pool, projectID, id int64) (Report, error) {
	p, err := Get(ctx, db, projectID, id)
	if err != nil {
		return Report{}, err
	}
	s, err := LoadSnapshot(ctx, db, projectID)
	if err != nil {
		return Report{}, err
	}
	return Analyze(p, s), nil
}
