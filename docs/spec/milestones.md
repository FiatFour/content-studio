# Milestones

Work only on the current milestone (named in CLAUDE.md). Each one ends with a verified demo and a short report.

---

## M0 — Assessment (stop and wait for approval)

Output is a single `docs/assessment.md` of **3 pages or fewer**, covering:
1. Repo state.
   - If it's greenfield, say so in one line.
   - Otherwise list what works, what's partial, mocked, or broken, and what's reusable.
2. Confirmed facts vs. assumptions. Keep them in separate lists.
3. The proposed M1a schema (tables and key columns only) and job stage list.
4. The top 5 technical risks, with mitigations.
5. Which items in `decisions.md` block M1a.

Also run a quick check on the machine:
- FFmpeg version.
- GPU visible.
- Whether the chosen LLM, ComfyUI checkpoint, and TTS load, with VRAM usage for each.
- `npx ffmpeg-skill doctor` (the owner has this skill installed; use it freely as a dev-time helper).

**Render backend spike (decides D11):** render the same 60 s fixture episode (stills + audio + Thai SRT) twice:
1. plain FFmpeg driven from a Go render spec;
2. the ffmpeg-skill tools (`render.py` / `caption.py` / `check.py`) called from Go.

Compare: control over pan/zoom and timing, Thai subtitle rendering, render time, error reporting, and how cleanly each fits behind a Go interface. Recommend one in the assessment.

Do not write application code in M0.

---

## M1a — Thinnest real vertical slice

Goal: one real, playable episode video, produced locally, approved by hash, and exported.

Pipeline (one Fictional Series workflow, run sequentially by the worker):
1. **Script**
   - The LLM turns the idea into a JSON scene list: scenes, lines, speaker, and an image prompt per shot.
   - Validate it against a JSON Schema. On invalid output, retry up to N times, then fail with the reason.
2. **Images**
   - One ComfyUI workflow with one style checkpoint, one image per shot.
   - Character consistency is *not* required yet.
3. **Voice**
   - Local TTS for each line, with a fixed voice per character.
4. **Subtitles**
   - Built from script text plus measured audio durations, written as SRT.
   - Whisper alignment is optional.
5. **Render**
   - FFmpeg, 1080×1920, configurable fps.
   - Pan/zoom per shot, concatenated audio, burned-in subtitles.
6. **QA (deterministic)**
   - ffprobe confirms the file decodes, dimensions and aspect are correct, duration is within ±10% of target, and audio and video streams are present.
   - No missing assets and no zero-length audio.
7. **Artifact**
   - Store the final MP4 with its sha256, version, and parent references to the script and assets.
8. **Review page**
   - Play the video, read the script, and see QA results.
   - Approve or reject this exact version.
9. **Export**
   - Copy the approved file and verify the exported hash equals the approved hash.

In scope:
- Projects/episodes API.
- Persistent job and stage records.
- Per-stage checkpoint (a completed stage is not redone on resume).
- Localhost-only binding plus a single admin token.
- Every UI string goes through a TH/EN translation layer from day one (Thai default, TH/EN toggle in the top bar). A test fails if a key exists in one language but not the other.

Out of scope:
- Cloud providers, budget, character consistency, canon, auth beyond the token, 3D, a second profile.

**Acceptance:**
- `make dev` starts everything.
- Creating an episode through the UI produces an MP4 that passes QA.
- Approving and exporting gives a file whose hash matches the approved version.
- A re-render creates a new, unapproved version, and the old approval does not carry over.
- Killing the worker mid-job and restarting it either resumes from the last completed stage or marks the job failed with a reason. No corrupted state.
- `go test ./...` passes using fake providers.

---

## M1b — Characters and a minimal story bible

1. **Consistency spike first**
   - Compare two character-consistency techniques on this hardware, for example IP-Adapter with reference sheets vs. per-character LoRA.
   - Report quality, VRAM use, and time per image. Wait for a choice before building.
2. Add character records (name, appearance, personality, voice, reference images) and an episode summary per episode.
3. Feed the script stage only the relevant characters and recent summaries (structured retrieval, not the full history).
4. Add a simple deterministic check: every speaker in the script exists and is alive and present at the episode's point in time.

**Acceptance:**
- Three consecutive episodes keep each main character visually recognizable. This is judged by you; record the result.

---

## M2 — Reliability, security, cost

- Worker leases and heartbeats, stale-job recovery, error classification, bounded retries with backoff, cancellation, and idempotency keys (`jobs.md`).
- Proper auth and authorization on sensitive actions (`security-ops.md`).
- Provider registry, task policies, budget reservations and reconciliation, an emergency kill switch, and optionally the first cloud adapter (`providers-budget.md`).
- Discord webhook notifications: outbound-only, notify-never-act, event toggles, test message (`notifications.md`).
- Audit log.
- A backup **and a tested restore**.
- Failure and concurrency tests (list in `jobs.md`).

**Acceptance:**
- Restarts and recoverable failures don't corrupt jobs.
- Concurrent jobs can't overspend the budget.
- Cloud fallback can't bypass provider switches or budget.
- Approval can't be bypassed.
- A restore from backup produces a working system.

---

## M3 — Operations control center
- Job inspection and error explanations.
- Provider health, cost reports, resource monitoring.
- Artifact history and version comparison.
- Canon proposal workflow and QA reports.

Improve what exists; don't duplicate features.

## M4 — Agent tower visualization
A cutaway multi-storey "agent tower" (see `ui.md`): one floor per pipeline stage, episode crates on conveyors, a pipeline elevator, and your review office on the top floor. It visualizes **real** job and task state only.
- Show a disconnected state when the backend is down.
- Keep it optional: every core task must still work without it.
- Keep its GPU use light while local jobs are running.

## M5 — Second content profile
Knowledge Shorts. This proves the engine is profile-agnostic without major rewrites.

## Never
Autonomous publishing. Future publishing adapters may only prepare drafts, and every publish requires explicit human approval of an exact version.
