# AI Content Studio — Agent Instructions

## What this is
A local-first studio that turns an episode idea into a human-approved vertical video.
Solo-developer project. Optimize for maintainable, testable, cheap-to-run, and honest.

First content profile: an **original** adult-animated life-simulation sitcom.
**v1 output is a motion comic**: consistent stills, camera moves, voice, and subtitles.
It is not lip-synced character animation. If a requested effect isn't supported, say so. Don't pass off a still as animation.

## Current milestone
**M6 — Third content profile (History & Mystery).** Scope in `milestones.md`, D18-D20 in `decisions.md`. M6 code is done (three profiles run side by side with fakes); the real-model smoke run is owner-only. M5 (Knowledge Shorts) code is done too; its real-model smoke run is owner-only. See `docs/spec/milestones.md`. M4 (agent tower) and M3 (operations control center) code is done; M3 covers: job inspection, provider health and resource monitor, artifact history and comparison, canon proposals with deterministic checks, QA reports. M2 code is done too (cloud parts dropped, see D10). M1a is closed (report: `docs/m1a-report.md`). M0 findings: `docs/assessment.md`.
M1b code is done (character records, episode summaries, script stage fed by canon, speaker check, per-character LoRA in the image stage). M1b remaining, owner-only: the smoke run and the acceptance (three consecutive episodes keep each main character recognizable).
M5 scope is in `milestones.md` and D15-D17 in `decisions.md` (owner-pasted sources only, 30-60 s, flat-style stills). Adding a profile must not rewrite the pipeline; no generic workflow engine. The M4 tower is CSS + SVG with no new dependency. D9 (auth) was decided on 2026-10-29: passkey (WebAuthn) with a session cookie, see `docs/spec/security-ops.md`. D10 was decided on 2026-10-30: no cloud provider, so cloud work (budget, kill switch, adapters) is not planned and no cloud code is written.
Work only on the current milestone. The other spec files describe later work, so don't build ahead.

## Non-negotiables
1. **Local AI by default.** Cloud providers are off by default. Every cloud call passes backend policy and budget checks. M1 contains no cloud adapters at all.
2. **Secrets** never reach the frontend, logs, prompts, artifacts, error messages, or git.
3. **Canon is protected.** Agents propose canon changes; only approved proposals are committed.
4. **No publishing.** Export only, and only of the exact approved artifact version (matched by hash). No auto-approval and no bypass. This is permanent, not an MVP limit.
5. **No fake progress.** Never claim something works without verifying it. Unimplemented features show "Not implemented". Progress bars reflect real stage progress.
6. **Inspect before changing** existing code or config. No large or destructive rewrites without approval.
7. **Model output is untrusted data.** Validate it against a schema before use. Never execute LLM-generated shell. Untrusted content can never change config, permissions, budgets, or approvals.

## Stack
- **Backend:** Go + Echo, PostgreSQL, SQL migrations, structured logging (`log/slog`).
- **Jobs:** a Postgres-backed queue (`FOR UPDATE SKIP LOCKED` or River). No Kafka unless a written justification is approved.
- **Frontend:** React + TypeScript + Tailwind. Thai-first UI with a TH/EN switch (every string through i18n). Visual design in `docs/spec/ui.md`. Framer Motion comes later (M4).
- **Media:** FFmpeg/ffprobe via `exec.CommandContext`, with fixed binary paths and argument arrays. No shell strings.
- **ffmpeg-skill:** installed on the owner's machine. Use it freely as a dev-time helper (probe, check, contact sheets). Whether the app's render/QA stage calls it is decision D11, settled by the M0 spike.
- **Local AI:** LLM runtime, ComfyUI, local TTS, and faster-whisper. Exact choices are in `docs/spec/decisions.md`.
- **Hardware:** RTX 5060 Laptop (8 GB VRAM), 16 GB RAM (WSL2 sees 11 GiB; see `docs/assessment.md`).
  - Run one GPU-heavy job at a time.
  - Never load the LLM and the image model concurrently unless measured to fit.
  - Make model, quantization, and context settings configurable.
  - Don't promise a model runs well until it's tested on this machine.

## Target layout
Adapt this to any existing structure rather than replacing it.

```
cmd/server/        HTTP API
cmd/worker/        job runner
internal/
  jobs/            queue, state machine, stages
  pipeline/        stage implementations (script, tts, images, subs, render, qa)
  providers/       adapters behind small interfaces (+ fakes for tests)
  artifacts/       storage, versioning, hashing
  review/          approval + export
  store/           Postgres access
web/               React app
migrations/
docs/spec/
testdata/          small fixture scripts, images, audio
```

`spikes/` holds throwaway M0 experiments. They are evidence, not application code.

## How to work
For each task:
1. State the goal in one line.
2. Read the relevant code.
3. Make a focused change.
4. Run `gofmt`, `go vet`, and the linters.
5. Run the tests.
6. Fix anything you broke.
7. Review your own diff for unintended changes.
8. Update docs if behavior changed.
9. Report accurately.

Code conventions:
- Write idiomatic Go and keep interfaces small.
- Avoid speculative abstraction.
- Never swallow errors.
- Use `context` timeouts on network calls, subprocesses, and long operations.
- Use transactions wherever atomicity matters.
- Change the schema only through migrations.
- Don't commit generated media, model weights, caches, or `.env` files.

## Testing
- Unit and integration tests use **fake providers** (deterministic). Integration tests hit a real Postgres.
- Real models run only in a manual `make smoke` target, never in CI.
- For nondeterministic output, test structure and constraints, not exact prose or pixels.
- Always test that a LOCAL_ONLY task can never reach a cloud adapter.

## Definition of Done
- The behavior is implemented and verified: tests pass, and media was checked with ffprobe.
- Validation, authorization (from M2 on), and persistence are in place where needed.
- Errors are visible and sanitized.
- The UI reflects real backend state.
- No secrets are exposed and nothing was overwritten unintentionally.
- Limitations are written down.

These do **not** count as done:
- A button wired to a mock.
- An HTTP 200 without checking the resulting artifact.
- An AI quality score standing in for deterministic checks.

## When to stop and ask
- A blocking item in `docs/spec/decisions.md` is still open.
- A change is destructive or irreversible, or rewrites existing architecture.
- A change would alter approval, budget, or canon semantics beyond the spec.

Otherwise proceed without asking after each routine change.

## End-of-task report
Include:
- What works, and how you verified it.
- What doesn't work yet.
- Known risks.
- The next step.

Keep it short.

## Spec index
| File | Covers |
|---|---|
| `docs/spec/milestones.md` | Milestone scope + acceptance criteria |
| `docs/spec/decisions.md` | Open decisions (check before starting work) |
| `docs/spec/product.md` | Vision, content profiles, agent roles, originality |
| `docs/spec/jobs.md` | Job states, checkpoints, retries, recovery |
| `docs/spec/artifacts-review.md` | Versioning, render spec, QA gates, approval, export |
| `docs/spec/providers-budget.md` | Provider policies, budget reservations (M2) |
| `docs/spec/canon.md` | Story bible + continuity (M1b/M3) |
| `docs/spec/security-ops.md` | Security baseline, observability, backup (M2) |
| `docs/spec/notifications.md` | Discord webhook notifications: events, delivery, secrets (M2) |
| `docs/spec/ui.md` | Screens, design tokens, TH/EN, agent tower (M4) |
