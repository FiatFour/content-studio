# Artifacts, Rendering, QA, Review, and Export

## Artifacts
Every important output is a versioned, immutable artifact: script, shot plan, images, audio, subtitles, render spec, final video, QA report.

Each artifact records:
- ID, project, run, job.
- Type and version.
- Created timestamp.
- Parent artifact IDs (lineage).
- Storage path (always inside the configured storage root).
- sha256 of the content.
- Producing provider, model, and workflow metadata.
- Validation status.
- Source and license info for any external or generated asset.

Rules:
- Never overwrite an artifact file. A change creates a new version with lineage.
- Files live under a storage root with paths built by the app (IDs only), never from user or model input.

### History and comparison (read-only)
- `GET /api/episodes/:id/artifact-history[?type=]` lists every version of an episode (newest first, at most 1000) with type, version, sha256, parent IDs from `artifact_parents`, and approval status. Approval status is the latest `approvals` row for that artifact whose recorded sha256 equals the artifact's sha256; a decision recorded against any other hash shows "none".
- `GET /api/artifacts/compare?a=&b=` compares two versions of the same episode and type. JSON artifacts (script, shot plan, render spec) get a structured diff computed on the server (JSON-pointer paths, added/removed/changed, at most 500 entries, values shortened, files over 1 MiB refused). Images, video and audio are shown side by side by the web UI through `/api/artifacts/:id/content` (same limits, ranges and security as before). SRT is shown as two text panes.
- UI: `#/history/:episodeId` and `#/compare/:a/:b`. Artifact content is rendered as text only. These views have no approve, export or re-render controls; those stay on the review screen.
- Limitations: array diffs compare by index (an inserted shot shifts later entries); no word-level text diff.

### Sources (Knowledge Shorts, D15)
- A source is owner-pasted plain text, stored as artifact type `source` (file `.txt`) attached to an episode whose project profile is `knowledge_shorts`; any other profile is refused with 409. Nothing is fetched, searched or dereferenced: the optional citation note is text only. Title and citation live in the artifact's `meta`.
- Stable id: the item key `s1`, `s2`, ... (per-episode counter, assigned under the episode advisory lock; sources are never deleted, so ids are never reused). Each edit is a new version of the same item key with the previous version as its `artifact_parents` parent. Rows are immutable (0011 triggers); files are never overwritten.
- Migration 0019 makes `artifacts.run_id` and `job_id` nullable, with a CHECK that only type `source` may leave them NULL (the owner creates a source, not a stage). Run-based views are unaffected; `artifact-history` reports `run_id` 0 for a source.
- `GET /api/episodes/:id/sources` (latest version of each source with id, sha256, size, all version ids), `POST /api/episodes/:id/sources` (create), `POST /api/episodes/:id/sources/:source/versions` (new version; an unchanged resubmission is 409). Text is read through `GET /api/artifacts/:id/content` as `text/plain` with `nosniff`.
- Validation: valid UTF-8; CRLF normalized to LF; no control characters except newline and tab (NUL, ESC, bare CR, C1 refused); text at most 200 KB (413), at most 20 sources per episode (409; new versions still allowed), title 1 to 120 characters without control characters, citation at most 500. Constants live in `internal/api/sources.go`.
- Audit: `source.create` / `source.version` rows are written in the same transaction as the artifact; detail holds scalars only (episode id, source id, version, sha prefix, size), never the text, title or citation. If the audit insert fails, the artifact row rolls back and the file is removed.
- UI: a Sources card on the episode page, shown only for `knowledge_shorts`; all text is rendered as React text. No such episode can be created in the UI yet (the profile is not runnable); verified with seeded data.
- Limitations: no source deletion or archiving; the claim script stage (T4) cites sources by item key and checks quotes against the latest version only (see product.md); the file of a source is not re-hashed on read.

## Render spec
The video is described by a versioned JSON timeline, then executed by FFmpeg. Don't hardcode scenes into one giant command.

The timeline contains:
- Scenes and shots, each with duration.
- Image (or video) asset per shot.
- Camera move per shot (pan/zoom preset and parameters).
- Transition type.
- Dialogue audio clips and their start times.
- Music and SFX tracks (if any).
- Subtitle file and style.
- Output parameters: 1080×1920, fps, codecs, bitrate.

Validate the spec before rendering: all assets exist and hash-match, durations are positive, audio covers the dialogue.

Use reusable presets for camera moves, transitions, and subtitle styles. If an effect isn't supported, report it rather than faking it.

## QA gates

### Deterministic (blocking)
- Output file exists and decodes (ffprobe).
- Dimensions and aspect ratio are correct.
- Duration is within tolerance of the target.
- Frame rate matches.
- Video and audio streams are present.
- No missing assets; timeline is valid.
- Subtitle file present when required.
- Render process exited 0.
- Enough free disk space.

### Deterministic (warning, when detectable)
- Black frames (`blackdetect`).
- Long silences (`silencedetect`).
- Audio clipping or excessive loudness.
- Subtitle/audio drift.

### AI-assisted (advisory only)
Script completeness, continuity, character consistency, style consistency, scene-to-script match, unwanted text in images.
An AI score never replaces the blocking checks or human review.

A failed blocking check prevents the video from being marked ready for review.
QA results record rule versions, timestamps, and artifact IDs, with a clear reason and a suggested fix for each failure.

### Knowledge Shorts rules (rule set `qa-rules-shorts-v1`, M5 T6)
The QA stage is shared. After the checks above it asks whether the run's script artifact is a Knowledge Shorts script (its meta says `knowledge_shorts`) and, if so, adds these rules (small rule list in `internal/pipeline/shorts_qa.go`, not a forked stage). The job's recorded model label is the profile's `QARules`. Every rule is deterministic, recorded in `qa_results` with rule id, version 1, severity, reason and suggested fix.
- `shorts_duration_range` (blocking): the video is 30 to 60 s (the profile range) and within 10% of the episode target. The shared `duration` rule still runs.
- `shorts_sources_intact` (blocking): every source version named in the script's meta (by artifact id) still exists, has the recorded sha256 and its file hashes to it. A missing, rewritten or removed source fails.
- `shorts_claims_quoted` (blocking): `CheckShortsClaims` is run again against exactly those source versions: every claim names an existing source and its quote is in that source word for word (whitespace aside). A script with no claims fails.
- `shorts_beats_supported` (blocking): every beat lists claims or is marked `no_factual_claim`, and the two are not mixed.
- `shorts_sources_card` (blocking): the subtitle file holds the Sources card heading (zero-width spaces from Thai segmentation ignored).
- `shorts_sources_current` (warning): a newer version of a used source exists. The script is still checked against the versions it was made from.
The claim rules prove the quote is in the source, not that the claim is true. The review page says so and the owner decides. Approval and export are unchanged: bound to the exact artifact id and sha256, explicit click, no AI input.

### History & Mystery rules (rule set `qa-rules-history-v1`, M6 T6)
Same shared QA stage: when the script artifact's meta says `history_mystery`, the rules in `internal/pipeline/history_qa.go` are added (the profile stays spec-only until M6 T7, so no run reaches them yet; they are tested on stored scripts). All deterministic, version 1. The script stage only validates the schema, narration length and quotes; the hedge and number checks of `CheckHistoryScript` run for the first time here.
- `history_duration_range` (blocking): 60 to 120 s and within 10% of the target.
- `history_narration_budget` (blocking): narration characters are inside the bounds for the speech time left after the lead, tail, two cards and the gaps (`shortsSpeechSeconds`, the timeline constants), recomputed from the stored script.
- `history_sources_intact` (blocking), `history_sources_current` (warning): as for Shorts.
- `history_claims_quoted` (blocking): every quote of every claim source is in its named source; a claim without sources or a script without claims fails.
- `history_beats_supported` (blocking): every beat lists claims or is marked `no_factual_claim`, not both.
- `history_uncertainty_labelled` (blocking): a beat citing a `speculation` or `disputed` claim has a hedging phrase from a visible list (`historyHedgeMarkers`). A phrase-list heuristic: it does not check the phrase hedges the right sentence.
- `history_numbers_in_sources` (warning): each number in the narration appears literally in some source. Literal only (no number words, no era or unit conversion), so it advises rather than blocks.
- `history_sources_card` (blocking): the subtitle file has the Sources card heading.
- `history_stills_labelled` (blocking): every still the render used has metadata `image_kind=illustration` and `evidence=false`. A still not found, or unlabelled, fails; a render with no stills passes (nothing to mislabel).
Approval and export are unchanged: exact artifact id and sha256, explicit click.

## Review
The review page shows:
- Video player.
- Script and subtitles.
- Scene and shot breakdown.
- QA results and continuity warnings.
- Cost (from M2).
- Version history.
- For a Knowledge Shorts video only: the sources used (id, title, version, short sha256), each claim with its source id, the beats that state it and its quote, and the stored claim check, all as plain text, with a note that the check proves the quote is in the source, not that the claim is true. `GET /api/videos/:id/review` adds a read-only `shorts` object for that profile only (the Fictional Series response is unchanged); it carries no file paths. If the stored script file is missing or no longer matches its hash, `shorts.unreadable` says so and no claims are shown.
- For a History & Mystery video only: the sources used, each claim with a certainty badge (documented, disputed, speculation; mark and word, not colour alone), date text, source quotes and the beats that state it, the stored claim check, the findings of `CheckHistoryScript` run again on the stored script and the hash-checked source texts, and a note that the stills are illustrations, not evidence, and that the checks do not prove a claim true. `GET /api/videos/:id/review` adds a read-only `history` object for that profile only (same unreadable handling and no file paths as `shorts`).

Actions: **Approve**, **Reject (with comment)**, **Request changes**.
Approval is always an explicit click. Opening, watching, or downloading never implies approval.

## Approval record
- Artifact ID, version, and sha256.
- Reviewer.
- Timestamp.
- Decision and notes.

Approval belongs to that exact version. Any new version starts unapproved.

## Export
- Export only an approved version.
- Re-hash the file at export time; if it doesn't match the approved hash, refuse.
- Never re-render or substitute a newer version during export.

## Publishing
Not implemented, and never automatic. Any future publishing adapter may only prepare a draft and must require explicit human approval of the exact version. No agent can approve anything.
