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

## Review
The review page shows:
- Video player.
- Script and subtitles.
- Scene and shot breakdown.
- QA results and continuity warnings.
- Cost (from M2).
- Version history.

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
