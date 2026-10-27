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
