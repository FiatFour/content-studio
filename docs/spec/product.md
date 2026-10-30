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

**Status (M5 T5):** the shared `tts`, `images`, `subtitles` and `render` stages now run a Knowledge Shorts script; none is forked. `loadScript` recognises a script artifact whose meta says `knowledge_shorts` and adapts it (`shorts_visuals.go`) into the shot and line inputs those stages already read: one `narrator` character (voice from `Spec.Visual.NarratorVoice`), a title card, one shot per beat (the line is the beat's narration), and a closing Sources card. Per-profile config is `Spec.Visual` (flat infographic style prefix, negative prompt, narrator voice, Sources heading, title cap). The images stage, given an adapted script, builds the prompt as style prefix plus the beat narration, uses the profile's negative prompt, and never passes a LoRA, trigger token or character look and never reads canon. Narration, titles and source titles are cleaned (control characters, braces, angle brackets and backslashes removed, whitespace collapsed, length capped) and a prompt naming an existing franchise falls back to a plain background. Cards have no spoken line: their text is a subtitle cue over the shot's span (same SRT and burn-in as dialogue), the title card first and the Sources card last, listing up to four source titles from the stored source artifacts the script was written from, then "and N more". `Spec.PlannedStages` is `script, tts, images, subtitles, render` and `Resources` marks script, tts and images as `gpu` (render none), and `Profile.Stages(d)` implements them, but `Spec.Stages` is still empty: the profile is not runnable and the API still answers 501 "Not implemented" until T6 adds the QA stage. (T6 added it; see below.)

**Limitation (T5):** output is still images with camera moves (a motion comic), not animation. Cards and infographic stills are text-free images with the text burned in as subtitles at the same bottom position and size as narration, so they are not designed layouts. Thai glyph rendering follows the D11 decision and the subtitle font setting. The cards add about 5 s of fixed time and each beat gap is 0.7 s, so narration must run about 13 s short of the target; the voice stage measures this and sends the script stage a rewrite note when the video would miss the 85-110% fit, but the first-guess prompt did not account for it (fixed in T6, below). Beat narration is Thai and is sent to the image model as is; whether a local image model draws sensible infographics from Thai text is untested. Tested with fake LLM, image, voice and segmenter providers and real ffmpeg; no real model has run for this profile.

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

**Status (M5 T6):** the shared `qa` stage now adds the Knowledge Shorts rule set `qa-rules-shorts-v1` for a Shorts script (rules in `artifacts-review.md`, code in `internal/pipeline/shorts_qa.go`): range 30-60 s, sources unchanged by artifact id and hash, every claim quoted from its source (the T4 check is re-run on the exact source versions), every beat supported, Sources card present, and a warning for newer source versions. `qa` is in `Spec.PlannedStages` and `Profile.Stages`; `Spec.Stages` is still empty, so the profile is not runnable and the API still answers 501 until T7 wires the multi-profile queue and worker. The review page (`GET /api/videos/:id/review` and `Review.tsx`) shows, for this profile only, the sources used, the claims with source and quote, and the claim check, with a note that the check proves the quote is in the source, not that the claim is true. No new route, approval or export path.

**Fix (T6):** the Shorts script prompt and `ValidateShortsScript` now budget narration from the target minus the real timeline overhead (lead and tail 1.5 s, title and Sources cards 2.5 s each, 0.7 s between each pair of the beats+2 shots: 12.8 s for 8 beats) at 9 to 18 characters per second of the remaining time. The prompt assumes 8 beats (the most it asks for) and asks for 10 to 14 characters per second; validation uses the real beat count. Prompt template version is 2.

**Limitation (T6):** QA still cannot say a claim is true or fairly summarised; the owner reads the claims. The script stage reads the latest source version, and QA checks the versions the script recorded, so a source edited after the script only warns (the script is not stale-failed). Characters per second is an estimate; the tts fit check still measures the real audio. No real model or real voice has run for this profile; tested with fakes and real ffmpeg.

**Status (M5 T7, acceptance):** `knowledge_shorts` is runnable. `Spec.PlannedStages` is gone; its stages (`script, tts, images, subtitles, render, qa`; re-render is `render, qa`) are `Spec.Stages`. The queue takes `Queue.Plans` (profile id to plan, `pipeline.RunnablePlans()`): a run's plan is found from `workflow_runs.profile` (`CreateRun` reads the episode's project profile), and the worker takes `Worker.ProfileStages` (profile id, then stage name, to implementation, `Deps.RunnableStages()`) because the stage names are shared but the implementations are not. A run whose profile has no plan fails at once with class `unsupported` and the fixed message "unknown content profile"; it never falls back to Fictional Series. The older single-plan form (`Queue.Plan`, `Worker.Stages`) still works unchanged and is what the older tests use. Both `cmd/server` and `cmd/worker` use the multi-profile form. The episode page and the tower take the stage list from the run's own profile.

API: creating a Shorts episode creates the episode only (the response has `run_id: null`), because sources are added to an episode that exists. `POST /api/episodes/:id/run` starts the first full run of an episode that has none; for Shorts it answers 409 "add at least one source before running a Knowledge Shorts episode" and creates no job when no source exists. Fictional Series still gets its run at creation. The episode page shows a "Start this episode" card for a Shorts episode with no run. An unknown profile is a 400; a profile with no stages, or one the queue has no plan for, is still a 501.

Verified with fakes (fake LLM, images and voice; real Postgres and ffmpeg; `internal/pipeline/multiprofile_test.go`, `internal/api/multiprofile_test.go`): one Fictional Series and one Shorts episode run through two real worker loops at once, both reach `ready_for_review` with a `qa_passed` video, each with its own artifacts and lineage (video, render spec, images, script; Shorts script also has its source versions as parents), the single `gpu` slot is never held by two jobs at once (checked by job time overlap and by the peak of in-flight provider calls), a Shorts run that fails script validation leaves the Fictional Series run untouched, a re-render works for both and leaves version 1 unchanged, approval and export of each are bound to its exact sha256 (a stale hash and an unapproved version are refused), the cloud spy sees 0 calls, and a run of an unknown profile fails permanently. Browser check (verify-content-studio flow `shorts-episode`, throwaway database, no worker): create a Shorts episode in the UI, see the pending state, the refusal with no source, add a source, start, and a queued script job with the six Shorts stages.

Not verified: no real LLM, voice, image model or Thai font run for Shorts (owner smoke, separate issue); the Shorts narration budget is an estimate from characters per second, not measured against a real voice; the claim check proves each quote is in its source, not that the claim is true or fairly summarised.

**How to run a Knowledge Shorts episode:**
1. In Episodes, choose the Knowledge Shorts profile, enter a title, a target of 30 to 60 s and the topic or question, then create the episode.
2. On the episode page, add at least one source in the Sources card: paste the text and give it a title (the app never fetches links or searches the web). Add more sources or new versions as needed.
3. Press "Start run". The worker writes the script from your sources only, then makes voice, images, subtitles, the video and the QA report.
4. Open the review page. Read the sources used, every claim with its source and quote, and the claim check. Approve only if you agree the claims are true.
5. Export the approved version. Export is of that exact version by hash; there is no publishing.
