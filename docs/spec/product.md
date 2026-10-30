# Product

## Vision
A studio that coordinates specialized AI roles to turn an idea into a finished vertical video, ready for human review.
Long-term goal: one episode per day once the pipeline is reliable. Quality and stability come first; the daily target never justifies cutting them.

## First profile: Fictional Series
An original life-simulation sitcom. Characters are followed from birth through childhood, adulthood, family, aging, and later generations.

The series needs:
- Long-term character development and generational family trees.
- Persistent personalities and appearances.
- Timeline continuity.
- Comedy, drama, conflict, and life lessons.
- Recurring characters and locations.
- A consistent visual identity.

Format: vertical 9:16, about 1–2 minutes (v1 target is set in `decisions.md` D7).

### Visual style
Original Western adult-animated sitcom aesthetic:
- Thick outlines, cel shading, colorful environments.
- Expressive faces and body language.
- Readable on a phone screen.
- Comedic timing and cinematic framing.

### Originality
Do not copy existing copyrighted characters, franchises, scripts, logos, or distinctive designs. Never put existing show or character names in image prompts.

## Content profiles
The engine is shared; profiles plug into it. Adding a profile must not require rewriting the pipeline.

Planned profiles:
1. **Fictional Series** (first).
2. **Knowledge Shorts** (M5).
3. History & Mystery (later).
4. Documentary (later).

A profile defines:
- Its stage list and per-stage settings.
- Script schema.
- Research requirements (nonfiction only).
- Visual style and asset requirements.
- Voice and subtitle settings.
- Video format and duration target.
- QA rules.
- Review requirements.
- Optional profile-specific state (e.g. the story bible for Fictional Series).

Until M5, implement profile config as plain data plus a small Go interface. Don't build a generic workflow engine ahead of the second profile.

**Status (M5 T1):** `internal/pipeline/profile.go` holds the abstraction. `Spec` is plain data: id, stage list per run kind (`full`, `rerender`), resource map, script schema name, duration range, QA rule set name, and review config. `Profile` is a small interface (`Spec()` plus `Stages(*Deps)`, the stage implementations), and `Profiles` is a registry (`Get` by id, `IDs`). Fictional Series is the first profile; `jobs.Plan` is derived from its spec (`Spec.Plan()`), with identical values to before. Not included on purpose: no workflow engine, no DB schema change (T2), no second profile, no research, voice or visual-style settings yet, and the script schema, QA rules and `MaxAutoRewrites` are still enforced by the shared code (the spec names them; it does not yet drive them). Canon and project lookups remain Fictional Series specific.

**Status (M5 T2):** `knowledge_shorts` is registered as a spec only (length 30-60 s, no stages, `Spec.Runnable()` is false). `POST /api/episodes` takes an optional `profile` (default `fictional_series`); an unknown id is a 400, the length is checked against that profile's range (the Fictional Series message is unchanged), and a profile that is not runnable gets HTTP 501 "Not implemented" before anything is written (no project, episode or run). Each profile has one project, found or created on first use (`ensureProject(profile)`); `GET /api/episodes` and `GET /api/episodes/:id` return the profile id. Migration 0018 widens the `episodes.target_duration_s` CHECK to 30-120 as the database backstop; the exact per-profile range is enforced in Go from the Spec. Canon, character and proposal endpoints are still fictional_series only and always use that project (they take no project or profile, so they never touch another profile's project). The new-episode form has a profile selector; Knowledge Shorts is shown as "Not implemented" and cannot be submitted. The episodes list and detail show the profile. The Characters and Canon pages do not scope to a project, so they have no "not applicable" note yet. Run stage lists on the episode page (`loadRuns`) and the server/worker queue plan are still the Fictional Series ones until T3 to T6.

**Status (M5 T3):** Knowledge Shorts sources are stored as versioned, hashed, immutable `source` artifacts with API and an episode-page Sources card; details and limits in `artifacts-review.md` (Sources). The profile is still not runnable; no claims, research or fetching exist.

## Agent roles
These are **logical roles**, not separate running models. Most map to a pipeline stage that calls one shared LLM runtime with a different prompt and schema.

| Role | Stage | Notes |
|---|---|---|
| Story planner | concept / outline | Later; M1a goes straight from idea to script. |
| Scriptwriter | script | JSON scene list. |
| Continuity checker | continuity | Deterministic checks first, LLM review as advisory. |
| Storyboard | shot plan | Can be merged into the script stage in M1a. |
| Visual assets | images | Prompt building plus ComfyUI. |
| Voice | tts | |
| Subtitles | subs | |
| Assembly | render | Builds the render spec; FFmpeg executes it. |
| QA | qa | Deterministic checks gate; AI review is advisory only. |
| Research | research | Nonfiction profiles only. |

**Deterministic code, never an LLM, owns:** job state transitions, policy and budget decisions, permission checks, hashing, approvals, and export.

### Agent task contract
Every LLM-backed task records:
- Task ID, stage, provider, model, model version.
- Input and output JSON Schemas.
- The prompt (or prompt template ID plus variables).
- Timeout and retry policy.
- Error class on failure.

Output is validated against its schema before the next stage sees it. Malformed or partial output fails the task cleanly; it never corrupts downstream state.

Agents have no shell access, no secrets, no provider/config access, no DB admin, and no publishing ability.
