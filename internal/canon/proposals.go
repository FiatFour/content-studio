package canon

// Canon proposals (M3). A stage, agent or HTTP caller can only Create a proposal, which writes nothing
// to canon. Approve is the only code that commits a proposed change, and it commits the change, the
// decision row and the audit row in one transaction. Every proposal needs an explicit owner decision:
// there is no auto-approve and no policy that skips it. Payloads are model output and so untrusted:
// they are decoded strictly (unknown fields refused) and validated here before they are stored and
// again before they are committed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/audit"
)

const (
	KindCharacterCreate = "character.create"
	KindCharacterUpdate = "character.update"
	KindRelationshipAdd = "relationship.add"
	KindFactAdd         = "fact.add"

	StatusProposed = "proposed"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// RelationshipTypes mirrors the CHECK constraint in migrations/0017.
var RelationshipTypes = []string{"parent", "child", "sibling", "spouse", "partner", "friend", "rival", "coworker", "neighbor"}

var (
	// ErrInvalid wraps a message that is safe to show: the payload or decision failed validation.
	ErrInvalid = errors.New("invalid canon proposal")
	// ErrDecided means the proposal already has a decision.
	ErrDecided = errors.New("canon proposal already decided")
	// ErrConflict means the change cannot be committed against the current canon (e.g. duplicate slug).
	ErrConflict = errors.New("canon proposal conflicts with current canon")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func conflict(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, a...))
}

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// Proposal is one stored proposal with its derived status and decision (nil fields while proposed).
type Proposal struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	Rationale  string          `json:"rationale"`
	ProposedBy string          `json:"proposed_by"`
	EpisodeID  *int64          `json:"episode_id"`
	CreatedAt  time.Time       `json:"created_at"`
	Status     string          `json:"status"`
	DecidedBy  *string         `json:"decided_by"`
	Reason     *string         `json:"reason"`
	DecidedAt  *time.Time      `json:"decided_at"`
}

// NewProposal is the input to Create.
type NewProposal struct {
	ProjectID  int64
	Kind       string
	Payload    json.RawMessage
	Rationale  string
	ProposedBy string // who or what proposes: an owner actor, or e.g. "stage:script"; never empty
	EpisodeID  int64  // 0 means none
}

type characterFields struct {
	Name        string `json:"name"`
	Appearance  string `json:"appearance"`
	Personality string `json:"personality"`
	Voice       string `json:"voice"`
	ArrivesOn   string `json:"arrives_on"`
	Status      string `json:"status"`
	StatusSince string `json:"status_since"`
}

// CharacterCreatePayload is the payload of kind character.create. Dates are YYYY-MM-DD or empty.
// LoRA settings are deliberately not proposable: they are configuration, set by the owner only.
type CharacterCreatePayload struct {
	Slug string `json:"slug"`
	characterFields
}

// CharacterUpdatePayload is the payload of kind character.update. Nil fields stay unchanged.
type CharacterUpdatePayload struct {
	Slug        string  `json:"slug"`
	Name        *string `json:"name,omitempty"`
	Appearance  *string `json:"appearance,omitempty"`
	Personality *string `json:"personality,omitempty"`
	Voice       *string `json:"voice,omitempty"`
	ArrivesOn   *string `json:"arrives_on,omitempty"`
	Status      *string `json:"status,omitempty"`
	StatusSince *string `json:"status_since,omitempty"`
}

// RelationshipAddPayload is the payload of kind relationship.add. From and To are character slugs.
type RelationshipAddPayload struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Type     string `json:"type"`
	StartsOn string `json:"starts_on"`
	EndsOn   string `json:"ends_on"`
}

// FactAddPayload is the payload of kind fact.add. SourceEpisodeID 0 means no source episode.
type FactAddPayload struct {
	Statement       string `json:"statement"`
	SourceEpisodeID int64  `json:"source_episode_id"`
}

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("payload does not match the schema")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return invalid("payload does not match the schema")
	}
	return nil
}

func between(s string, lo, hi int) bool { n := utf8.RuneCountInString(s); return n >= lo && n <= hi }

func date(field, v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	d, err := time.Parse(time.DateOnly, v)
	if err != nil || d.Year() < 1000 {
		return nil, invalid("%s must be a date like 2025-03-31", field)
	}
	return &d, nil
}

// validate trims and checks the character fields and returns the parsed dates. An alive character
// keeps no status_since, as in the owner's update path.
func (f *characterFields) validate() (arrives, since *time.Time, err error) {
	f.Name, f.Appearance, f.Personality, f.Voice = strings.TrimSpace(f.Name), strings.TrimSpace(f.Appearance), strings.TrimSpace(f.Personality), strings.TrimSpace(f.Voice)
	if f.Status == "" {
		f.Status = StatusAlive
	}
	switch {
	case !between(f.Name, 1, 60):
		return nil, nil, invalid("name must be 1 to 60 characters")
	case !between(f.Appearance, 5, 600):
		return nil, nil, invalid("appearance must be 5 to 600 characters")
	case !between(f.Personality, 0, 600):
		return nil, nil, invalid("personality must be at most 600 characters")
	case !between(f.Voice, 5, 200):
		return nil, nil, invalid("voice must be 5 to 200 characters")
	case f.Status != StatusAlive && f.Status != StatusDeceased && f.Status != StatusMovedAway:
		return nil, nil, invalid("status must be alive, deceased or moved_away")
	}
	if arrives, err = date("arrives_on", f.ArrivesOn); err != nil {
		return nil, nil, err
	}
	if since, err = date("status_since", f.StatusSince); err != nil {
		return nil, nil, err
	}
	if f.Status == StatusAlive {
		f.StatusSince, since = "", nil
	} else if since == nil {
		return nil, nil, invalid("status_since is required when the character is not alive")
	}
	return arrives, since, nil
}

func (p *CharacterUpdatePayload) hasChange() bool {
	return p.Name != nil || p.Appearance != nil || p.Personality != nil || p.Voice != nil || p.ArrivesOn != nil || p.Status != nil || p.StatusSince != nil
}

// validateFields checks each field that is present on its own; the merged result is checked again
// against the current character when the proposal is committed.
func (p *CharacterUpdatePayload) validateFields() error {
	str := func(v *string, lo, hi int, msg string) error {
		if v != nil && !between(strings.TrimSpace(*v), lo, hi) {
			return invalid("%s", msg)
		}
		return nil
	}
	for _, err := range []error{
		str(p.Name, 1, 60, "name must be 1 to 60 characters"),
		str(p.Appearance, 5, 600, "appearance must be 5 to 600 characters"),
		str(p.Personality, 0, 600, "personality must be at most 600 characters"),
		str(p.Voice, 5, 200, "voice must be 5 to 200 characters"),
	} {
		if err != nil {
			return err
		}
	}
	if p.Status != nil && *p.Status != StatusAlive && *p.Status != StatusDeceased && *p.Status != StatusMovedAway {
		return invalid("status must be alive, deceased or moved_away")
	}
	if p.ArrivesOn != nil {
		if _, err := date("arrives_on", *p.ArrivesOn); err != nil {
			return err
		}
	}
	if p.StatusSince != nil {
		if _, err := date("status_since", *p.StatusSince); err != nil {
			return err
		}
	}
	return nil
}

func (p *RelationshipAddPayload) validate() error {
	known := false
	for _, t := range RelationshipTypes {
		known = known || t == p.Type
	}
	switch {
	case !slugRe.MatchString(p.From) || !slugRe.MatchString(p.To):
		return invalid("from and to must be character ids")
	case p.From == p.To:
		return invalid("a character cannot have a relationship with itself")
	case !known:
		return invalid("type must be one of %s", strings.Join(RelationshipTypes, ", "))
	}
	s, err := date("starts_on", p.StartsOn)
	if err != nil {
		return err
	}
	e, err := date("ends_on", p.EndsOn)
	if err != nil {
		return err
	}
	if s != nil && e != nil && e.Before(*s) {
		return invalid("ends_on is before starts_on")
	}
	return nil
}

// normalize validates payload for kind and returns it re-encoded from the typed struct, so nothing
// outside the schema is stored.
func normalize(kind string, raw json.RawMessage) (json.RawMessage, error) {
	var v any
	switch kind {
	case KindCharacterCreate:
		var p CharacterCreatePayload
		if err := decodeStrict(raw, &p); err != nil {
			return nil, err
		}
		if !slugRe.MatchString(p.Slug) {
			return nil, invalid("id must be 1 to 16 characters: a lowercase letter, then lowercase letters or digits")
		}
		if _, _, err := p.characterFields.validate(); err != nil {
			return nil, err
		}
		v = p
	case KindCharacterUpdate:
		var p CharacterUpdatePayload
		if err := decodeStrict(raw, &p); err != nil {
			return nil, err
		}
		if !slugRe.MatchString(p.Slug) {
			return nil, invalid("slug must be a character id")
		}
		if !p.hasChange() {
			return nil, invalid("no field to change")
		}
		if err := p.validateFields(); err != nil {
			return nil, err
		}
		v = p
	case KindRelationshipAdd:
		var p RelationshipAddPayload
		if err := decodeStrict(raw, &p); err != nil {
			return nil, err
		}
		if err := p.validate(); err != nil {
			return nil, err
		}
		v = p
	case KindFactAdd:
		var p FactAddPayload
		if err := decodeStrict(raw, &p); err != nil {
			return nil, err
		}
		p.Statement = strings.TrimSpace(p.Statement)
		if !between(p.Statement, 5, 300) {
			return nil, invalid("statement must be 5 to 300 characters")
		}
		if p.SourceEpisodeID < 0 {
			return nil, invalid("source_episode_id is invalid")
		}
		v = p
	default:
		return nil, invalid("unknown kind")
	}
	return json.Marshal(v)
}

// Create stores a proposal with status proposed. It validates the payload and writes nothing to canon.
// This is the only call a stage or agent may use to ask for a canon change.
func Create(ctx context.Context, db *pgxpool.Pool, np NewProposal) (int64, error) {
	np.Rationale = strings.TrimSpace(np.Rationale)
	if np.ProposedBy == "" {
		return 0, errors.New("canon: proposed_by is required")
	}
	if !between(np.Rationale, 0, 600) {
		return 0, invalid("rationale must be at most 600 characters")
	}
	payload, err := normalize(np.Kind, np.Payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var ep *int64
		if np.EpisodeID != 0 {
			ep = &np.EpisodeID
			var one int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM episodes WHERE id = $1 AND project_id = $2`, np.EpisodeID, np.ProjectID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return invalid("episode does not belong to this series")
			} else if err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO canon_proposals (project_id, kind, payload, rationale, proposed_by, episode_id)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, np.ProjectID, np.Kind, payload, np.Rationale, np.ProposedBy, ep).Scan(&id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: np.ProposedBy, Action: "canon.proposal.create", TargetType: "canon_proposal", TargetID: id,
			Detail: map[string]any{"kind": np.Kind}})
	})
	return id, err
}

const proposalSelect = `SELECT p.id, p.kind, p.payload, p.rationale, p.proposed_by, p.episode_id, p.created_at,
	COALESCE(d.decision, 'proposed'), d.decided_by, d.reason, d.decided_at
	FROM canon_proposals p LEFT JOIN canon_proposal_decisions d ON d.proposal_id = p.id`

func scanProposal(r pgx.Row) (Proposal, error) {
	var p Proposal
	err := r.Scan(&p.ID, &p.Kind, &p.Payload, &p.Rationale, &p.ProposedBy, &p.EpisodeID, &p.CreatedAt, &p.Status, &p.DecidedBy, &p.Reason, &p.DecidedAt)
	return p, err
}

// Get returns one proposal of the project; pgx.ErrNoRows when it does not exist.
func Get(ctx context.Context, db *pgxpool.Pool, projectID, id int64) (Proposal, error) {
	return scanProposal(db.QueryRow(ctx, proposalSelect+` WHERE p.id = $1 AND p.project_id = $2`, id, projectID))
}

// List returns proposals newest first. status is "", proposed, approved or rejected.
func List(ctx context.Context, db *pgxpool.Pool, projectID int64, status string, limit int) ([]Proposal, error) {
	if status != "" && status != StatusProposed && status != StatusApproved && status != StatusRejected {
		return nil, invalid("status must be proposed, approved or rejected")
	}
	rows, err := db.Query(ctx, proposalSelect+` WHERE p.project_id = $1 AND ($2 = '' OR COALESCE(d.decision, 'proposed') = $2)
		ORDER BY p.id DESC LIMIT $3`, projectID, status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Proposal, error) { return scanProposal(r) })
}

// Approve commits the proposed change, records the decision and writes the audit row in one
// transaction. actor must be the authenticated owner identity. If the change cannot be committed
// against the current canon the whole transaction rolls back and the proposal stays proposed.
func Approve(ctx context.Context, db *pgxpool.Pool, projectID, id int64, actor, reason string) error {
	return decide(ctx, db, projectID, id, StatusApproved, actor, reason)
}

// Reject records the decision and the audit row. Canon is not touched.
func Reject(ctx context.Context, db *pgxpool.Pool, projectID, id int64, actor, reason string) error {
	return decide(ctx, db, projectID, id, StatusRejected, actor, reason)
}

func decide(ctx context.Context, db *pgxpool.Pool, projectID, id int64, decision, actor, reason string) error {
	reason = strings.TrimSpace(reason)
	if actor == "" {
		return errors.New("canon: decided_by is required")
	}
	if !between(reason, 1, 500) {
		return invalid("reason must be 1 to 500 characters")
	}
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var kind string
		var payload json.RawMessage
		// FOR UPDATE serializes concurrent decisions on the same proposal; UNIQUE(proposal_id) on
		// decisions is the backstop.
		if err := tx.QueryRow(ctx, `SELECT kind, payload FROM canon_proposals WHERE id = $1 AND project_id = $2 FOR UPDATE`, id, projectID).Scan(&kind, &payload); err != nil {
			return err
		}
		var decided bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM canon_proposal_decisions WHERE proposal_id = $1)`, id).Scan(&decided); err != nil {
			return err
		}
		if decided {
			return ErrDecided
		}
		if _, err := tx.Exec(ctx, `INSERT INTO canon_proposal_decisions (proposal_id, decision, decided_by, reason) VALUES ($1,$2,$3,$4)`,
			id, decision, actor, reason); err != nil {
			return err
		}
		if decision == StatusApproved {
			if err := commit(ctx, tx, projectID, id, kind, payload); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "canon.proposal." + decision, TargetType: "canon_proposal", TargetID: id,
			Detail: map[string]any{"kind": kind, "reason": reason}})
	})
}

func characterID(ctx context.Context, tx pgx.Tx, projectID int64, slug string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM characters WHERE project_id = $1 AND slug = $2`, projectID, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, conflict("character %q does not exist", slug)
	}
	return id, err
}

// commit applies one approved payload inside the decision's transaction. It is unexported: Approve
// is the only way in.
func commit(ctx context.Context, tx pgx.Tx, projectID, proposalID int64, kind string, raw json.RawMessage) error {
	if _, err := normalize(kind, raw); err != nil { // re-validate what is about to be committed
		return err
	}
	switch kind {
	case KindCharacterCreate:
		var p CharacterCreatePayload
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		arrives, since, err := p.characterFields.validate()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO characters (project_id, slug, name, appearance, personality, voice, arrives_on, status, status_since)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, projectID, p.Slug, p.Name, p.Appearance, p.Personality, p.Voice, arrives, p.Status, since)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return conflict("a character with id %q already exists", p.Slug)
		}
		return err
	case KindCharacterUpdate:
		var p CharacterUpdatePayload
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		var f characterFields
		var arrives, since *time.Time
		var cid int64
		err := tx.QueryRow(ctx, `SELECT id, name, appearance, personality, voice, arrives_on, status, status_since FROM characters
			WHERE project_id = $1 AND slug = $2 FOR UPDATE`, projectID, p.Slug).Scan(&cid, &f.Name, &f.Appearance, &f.Personality, &f.Voice, &arrives, &f.Status, &since)
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("character %q does not exist", p.Slug)
		}
		if err != nil {
			return err
		}
		if arrives != nil {
			f.ArrivesOn = arrives.Format(time.DateOnly)
		}
		if since != nil {
			f.StatusSince = since.Format(time.DateOnly)
		}
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&f.Name, p.Name)
		set(&f.Appearance, p.Appearance)
		set(&f.Personality, p.Personality)
		set(&f.Voice, p.Voice)
		set(&f.ArrivesOn, p.ArrivesOn)
		set(&f.Status, p.Status)
		set(&f.StatusSince, p.StatusSince)
		if arrives, since, err = f.validate(); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE characters SET name=$2, appearance=$3, personality=$4, voice=$5, arrives_on=$6, status=$7, status_since=$8 WHERE id=$1`,
			cid, f.Name, f.Appearance, f.Personality, f.Voice, arrives, f.Status, since)
		return err
	case KindRelationshipAdd:
		var p RelationshipAddPayload
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		from, err := characterID(ctx, tx, projectID, p.From)
		if err != nil {
			return err
		}
		to, err := characterID(ctx, tx, projectID, p.To)
		if err != nil {
			return err
		}
		s, _ := date("starts_on", p.StartsOn) // already validated by normalize
		e, _ := date("ends_on", p.EndsOn)
		_, err = tx.Exec(ctx, `INSERT INTO character_relationships (project_id, from_character, to_character, rel_type, starts_on, ends_on, proposal_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, projectID, from, to, p.Type, s, e, proposalID)
		return err
	case KindFactAdd:
		var p FactAddPayload
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		var ep *int64
		if p.SourceEpisodeID != 0 {
			ep = &p.SourceEpisodeID
			var one int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM episodes WHERE id = $1 AND project_id = $2`, p.SourceEpisodeID, projectID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return conflict("source episode does not exist")
			} else if err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO established_facts (project_id, statement, source_episode_id, proposal_id) VALUES ($1,$2,$3,$4)`,
			projectID, strings.TrimSpace(p.Statement), ep, proposalID)
		return err
	}
	return invalid("unknown kind")
}
