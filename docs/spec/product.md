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

**Status (M5 T4):** the Knowledge Shorts script stage exists (`NewKnowledgeShortsScriptStage`, `internal/pipeline/shorts_stage.go`) with its own strict schema (`shorts_script.schema.json`: title, language `th`, narration beats, claims). Each claim has an id, text, a source id (`s1`, ...) and a quote. Each beat lists its claim ids or is marked `no_factual_claim` (hook, transition, call to action). The stage reads the episode idea and the latest version of every source (passed to the model as delimited data, never as instructions), asks the local LLM through `providers.Resolve` (LOCAL_ONLY), validates the schema and Thai narration length (same characters-per-second bounds as Fictional Series, against the 30-60 s target), then runs the deterministic claim check (`CheckShortsClaims`, `shorts_claims.go`). Failures are fed back as data for up to `ScriptRetries` tries; after that the stage fails with `validation_failed` and stores nothing. A passing script is stored as a `script` artifact whose parents are the source versions used, with the check result inside it (`claim_check`) and the source versions in its meta. `Spec.PlannedStages` lists `script`, but `Stages` is still empty: the profile is not runnable and `POST /api/episodes` still answers 501 "Not implemented" until T5/T6 add every stage.

Claim check rules: the cited source id exists for the episode; the quote, after collapsing whitespace only (no case, punctuation or Unicode folding), appears verbatim in the latest version of that source; every beat has claim ids or `no_factual_claim` (not both); every claim id used by a beat exists; every claim is used by a beat; claim ids are unique. Failure codes: `source_unknown`, `quote_not_found`, `quote_wrong_source`, `beat_unsupported`, `beat_contradictory`, `beat_unknown_claim`, `claim_unreferenced`, `claim_duplicate_id`.

**Limitation (T4):** the quote check proves only that the quoted text is in the owner's source. It does not prove the claim is true, nor that the claim text summarises the quote fairly or in context; the owner still reviews every script. Narration length is estimated from character counts, not measured from audio (no voice stage yet). Tested with the fake LLM only; no real model has written a Knowledge Shorts script.

**Status (M5 T5):** the shared `tts`, `images`, `subtitles` and `render` stages now run a Knowledge Shorts script; none is forked. `loadScript` recognises a script artifact whose meta says `knowledge_shorts` and adapts it (`shorts_visuals.go`) into the shot and line inputs those stages already read: one `narrator` character (voice from `Spec.Visual.NarratorVoice`), a title card, one shot per beat (the line is the beat's narration), and a closing Sources card. Per-profile config is `Spec.Visual` (flat infographic style prefix, negative prompt, narrator voice, Sources heading, title cap). The images stage, given an adapted script, builds the prompt as style prefix plus the beat narration, uses the profile's negative prompt, and never passes a LoRA, trigger token or character look and never reads canon. Narration, titles and source titles are cleaned (control characters, braces, angle brackets and backslashes removed, whitespace collapsed, length capped) and a prompt naming an existing franchise falls back to a plain background. Cards have no spoken line: their text is a subtitle cue over the shot's span (same SRT and burn-in as dialogue), the title card first and the Sources card last, listing up to four source titles from the stored source artifacts the script was written from, then "and N more". `Spec.PlannedStages` is `script, tts, images, subtitles, render` and `Resources` marks script, tts and images as `gpu` (render none), and `Profile.Stages(d)` implements them, but `Spec.Stages` is still empty: the profile is not runnable and the API still answers 501 "Not implemented" until T6 adds the QA stage.

**Limitation (T5):** output is still images with camera moves (a motion comic), not animation. Cards and infographic stills are text-free images with the text burned in as subtitles at the same bottom position and size as narration, so they are not designed layouts. Thai glyph rendering follows the D11 decision and the subtitle font setting. The cards add about 5 s of fixed time and each beat gap is 0.7 s, so narration must run about 13 s short of the target; the voice stage measures this and sends the script stage a rewrite note when the video would miss the 85-110% fit, but the first-guess prompt does not yet account for it. Beat narration is Thai and is sent to the image model as is; whether a local image model draws sensible infographics from Thai text is untested. Tested with fake LLM, image, voice and segmenter providers and real ffmpeg; no real model has run for this profile.

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
