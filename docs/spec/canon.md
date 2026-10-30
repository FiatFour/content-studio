# Story Bible and Continuity

M1b builds the minimum (characters, episode summaries, one presence check). M3 builds the full proposal workflow.

## Data
- **Characters**: name, appearance description, reference images, personality, voice, birth date (in-story), life stage, status (alive, deceased, moved away…).
- **Relationships**: typed edges (parent, sibling, spouse, friend, rival…), with start and end dates.
- **Locations**: name, description, reference images.
- **Timeline events**: in-story date, participants, description, source episode.
- **Episodes**: in-story date, summary, characters present, open threads.
- **Established facts**: short statements tied to a source episode.
- **Canon changes**: proposed and approved, with provenance.

## Retrieval
Send each model call only what the task needs: the characters in this episode, their relationships, the last few episode summaries, and open threads relevant to them. Never send the whole history.

## Canon safety
Agents can **propose** changes. They can never write canon directly.

Workflow (M3):
1. A stage produces a proposed change (structured, not prose).
2. Deterministic code finds affected characters, relationships, episodes, and timeline events.
3. Deterministic code checks for conflicts with established facts.
4. The proposal and its impact are shown to the owner.
5. Important changes require approval; minor ones follow a configured policy.
6. Only approved changes are committed, with who approved them, when, and why.

Temporary scene details (what someone is wearing today) don't need approval but must never alter canonical facts.

## Deterministic checks
Run these in code, independent of the writing model:
- Character age matches the episode's in-story date and life stage.
- Relationships are consistent (no one is their own grandparent; spouses exist at the same time).
- Speakers in the script exist and are alive and present.
- Events are in chronological order.
- A script fact doesn't contradict an established fact (structured facts only; LLM review for free-text contradictions is advisory).
- Open threads are tracked and listed.

## Status: proposals and commit path (M3 T7, migrations 0016 and 0017)
Implemented: structured, append-only canon proposals with an owner-approved commit. Not implemented: the deterministic impact and conflict checks (#61) and the UI (#62). Locations and timeline events are out of scope for now.

**Rules.** Every proposal needs an explicit owner decision. There is no auto-approve, no "minor change" policy and no bypass. A stage or agent can only call `canon.Create`, which writes nothing to canon. Payloads are untrusted: decoded strictly (unknown fields and trailing data refused), validated in code, and validated again at commit.

**Kinds and payloads** (Go types in `internal/canon/proposals.go`):
| Kind | Payload | Commits to |
|---|---|---|
| `character.create` | `slug, name, appearance, personality, voice, arrives_on, status, status_since` | `characters` |
| `character.update` | `slug` plus any of the same fields (omitted = unchanged; merged result is validated) | `characters` |
| `relationship.add` | `from`, `to` (character slugs), `type` (parent, child, sibling, spouse, partner, friend, rival, coworker, neighbor), `starts_on`, `ends_on` | `character_relationships` |
| `fact.add` | `statement` (5 to 300 chars), `source_episode_id` (0 = none) | `established_facts` |

LoRA name and trigger token are not proposable; the owner sets them.

**Tables.** `canon_proposals` (immutable proposal: kind, payload, rationale, `proposed_by`, optional episode, time) and `canon_proposal_decisions` (one row per proposal, `UNIQUE(proposal_id)`: approved or rejected, `decided_by`, `reason` 1 to 500 chars, time). Status is derived: no decision row = `proposed`. Both refuse UPDATE, DELETE and TRUNCATE (`record_immutable`, as migration 0014). `character_relationships` and `established_facts` are append-only, carry a unique `proposal_id`, and a trigger refuses an INSERT unless that proposal has an approved decision, so a direct write from a stage fails in the database. Ending a relationship or retracting a fact is not designed yet.
Limitation: `characters` keeps the owner's existing direct edit endpoints (`POST/PUT /api/characters`), so the database cannot tell an owner edit from a stage write there; no stage code writes it.

**Go API** (`internal/canon`):
- `Create(ctx, db, NewProposal{ProjectID, Kind, Payload, Rationale, ProposedBy, EpisodeID}) (id, error)`. Validates, stores status proposed, audits `canon.proposal.create` in one transaction. `ProposedBy` must be non-empty (use `stage:<name>`).
- `Get(ctx, db, projectID, id)`, `List(ctx, db, projectID, status, limit)` (status "" or proposed/approved/rejected, newest first). Return `Proposal` with `Status`, `DecidedBy`, `Reason`, `DecidedAt`.
- `Approve(ctx, db, projectID, id, actor, reason)`: in ONE transaction locks the proposal, inserts the decision, commits the change and writes audit `canon.proposal.approved`. If the change cannot apply (for example the slug exists or a character is missing) everything rolls back and the proposal stays proposed.
- `Reject(ctx, db, projectID, id, actor, reason)`: decision row and audit `canon.proposal.rejected` only; canon is untouched.
- Errors: `ErrInvalid` (safe message, 400), `ErrDecided` (409), `ErrConflict` (409), `pgx.ErrNoRows` (404).
- Hook for #61: run the deterministic checks on the stored `Proposal.Payload` before showing it; the checks must not write canon. `commit` is unexported so `Approve` is the only commit path.

**HTTP** (under the default-deny session middleware, rate limits and Origin guard; the actor is the session identity): `POST /api/canon/proposals` `{kind, payload, rationale, episode_id}` returns 201 `{id}`; `GET /api/canon/proposals?status=&limit=`; `GET /api/canon/proposals/:id`; `POST /api/canon/proposals/:id/approve` and `/reject` with `{reason}` (required).
