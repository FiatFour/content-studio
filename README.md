# content-studio

A local-first studio that turns an episode idea into a human-approved vertical video (1080×1920 MP4).
Current milestone: **M2**, reliability, security and cost (M1a is closed, see `docs/m1a-report.md`; M1b code is done and awaits the owner's smoke run and 3-episode acceptance). Rules for contributors and agents are in `CLAUDE.md`; milestone scope is in `docs/spec/milestones.md`.

The v1 output is a **motion comic**: generated stills with pan/zoom, local Thai voice, and burned-in subtitles. It is not lip-synced animation.

## Pipeline

One run goes through these stages in order, each stored as a job row so a finished stage is not redone after a crash:

`script → tts → images → subtitles → render → qa`

Voice runs before images so a script of the wrong spoken length fails early. A `script_duration` failure starts a rewrite run with the measured reason as feedback, at most twice per chain. The final MP4 is stored as an artifact with its sha256. A human approves or rejects that exact version on the review page, and export copies the approved file and checks that the hash is unchanged. Nothing is published anywhere.

## Requirements

- Go (the Makefile expects it in `~/.local/go/bin`), Node/npm, Docker (for Postgres), FFmpeg/ffprobe.
- Local AI runtimes, installed separately: Ollama with `qwen3:8b`, ComfyUI with DreamShaper XL Turbo v2.1, and the Thai voice service in `services/thaivoice` (VoxCPM2). Choices and measurements are in `docs/spec/decisions.md` and `docs/assessment.md`.
- Hardware target: 8 GB VRAM GPU. Only one GPU-heavy job runs at a time.

## Commands

| Command | What it does |
|---|---|
| `make dev` | Starts Postgres (container `studio-pg`), ComfyUI, the voice service, the server and the worker. UI at <http://localhost:8080> (use `localhost`, not `127.0.0.1`: passkeys need a domain name). Logs in `var/logs`. |
| `go run ./cmd/server enroll` | Prints a one-time code (valid 10 minutes) that registers your first passkey. See "Signing in" below. |
| `make test` | `go test ./...` against a real Postgres, then the web tests. Uses fake providers; no GPU. |
| `make fmt` / `make vet` | `gofmt -l` and `go vet`. |
| `make build` | Builds `bin/` and the web bundle. |
| `make smoke` | Runs one real 60 s episode through the live stack and checks the MP4 with ffprobe. Needs `make dev` running. Manual only, never CI. |

Keep the laptop on AC power with sleep off while real jobs run. Windows has hard-reset under GPU load on this machine.

## Using it

1. Open the UI (Thai by default, TH/EN toggle in the top bar) and create an episode with a title, an idea, and a target length of 60 to 120 s.
2. Watch the run on the episode page. A failed run shows its reason.
3. When a video reaches review, play it, read the script and QA results, then approve or reject.
4. Export is available only for an approved version. A re-render creates a new, unapproved version.

Settings come from `STUDIO_*` environment variables (see `internal/config/config.go`). Generated media, storage and exports live under `var/` and are not committed.

## Signing in

1. Start the server, then run `go run ./cmd/server enroll` in a terminal on this machine. It prints a code such as `ABCDE-FGHIJ-KLMNO-PQRST` to that terminal only (never to the log, the database or the web).
2. Open <http://localhost:8080>, choose enrollment, enter the code within 10 minutes and create the passkey. This works once: while any passkey exists no code can add another one.
3. Add more passkeys later from a signed-in session. Sessions expire after 12 h idle or 7 days in total (`STUDIO_SESSION_IDLE`, `STUDIO_SESSION_MAX`).

`STUDIO_AUTH_RP_ID` (default `localhost`) and `STUDIO_AUTH_ORIGINS` (comma separated; default `localhost` and `127.0.0.1` on the server port and on 5173 for the Vite dev server) change the relying party and the allowed origins for remote access over https.

## Known limitations

- **Authentication is passkey-only and partly done (D9, #16).** Every API route except health and the sign-in routes needs a session, and state-changing requests need an allowed `Origin`. Not yet done (#17): HTTPS setup, CSRF tokens, general rate limits and request-size limits, security headers. There is one owner and no recovery flow: if you lose every passkey, see "Lost every passkey" in `docs/spec/security-ops.md`. `make smoke` needs `STUDIO_SESSION` (see `scripts/smoke.sh`).
- Characters are not yet kept visually consistent between shots. M1b has the character records, episode summaries, script retrieval and speaker check; D8 chose per-character LoRA: the image stage applies a character's LoRA (U-Net only, at most 2 per shot, strength `STUDIO_LORA_STRENGTH`, default 0.8) and its trigger token when they are set on the character. Training is still a manual process, and the LoRA file must already be in ComfyUI's loras folder (the app does not check; a missing file fails the image stage, with no fallback to the plain model). Acceptance is the owner's judgement over three consecutive episodes.
- Script length is often wrong on the first try, so a run may need one or two rewrite rounds.
- Cloud providers, budgets, canon, notifications and the agent tower are not implemented.

## Layout

`cmd/server` HTTP API, `cmd/worker` job runner, `internal/` (api, jobs, pipeline, providers, artifacts, review, store, media, config), `web/` React app, `migrations/`, `services/thaivoice`, `docs/spec/`, `testdata/`, `spikes/` (throwaway M0 experiments).
