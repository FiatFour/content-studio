# content-studio

A local-first studio that turns an episode idea into a human-approved vertical video (1080×1920 MP4).
Current milestone: **M1a**, the thinnest real vertical slice. Rules for contributors and agents are in `CLAUDE.md`; milestone scope is in `docs/spec/milestones.md`.

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
| `make dev` | Starts Postgres (container `studio-pg`), ComfyUI, the voice service, the server and the worker. UI at <http://127.0.0.1:8080>. Logs in `var/logs`. |
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

## Known limitations

- **No authentication.** The server binds to 127.0.0.1 and nothing else protects approve and export. Any local process, or a web page open in your browser, can call the API. Add auth before using this off a solo local machine (`docs/spec/security-ops.md`, decision D9).
- Characters are not kept visually consistent between shots (M1b).
- Script length is often wrong on the first try, so a run may need one or two rewrite rounds.
- Cloud providers, budgets, canon, notifications and the agent tower are not implemented.

## Layout

`cmd/server` HTTP API, `cmd/worker` job runner, `internal/` (api, jobs, pipeline, providers, artifacts, review, store, media, config), `web/` React app, `migrations/`, `services/thaivoice`, `docs/spec/`, `testdata/`, `spikes/` (throwaway M0 experiments).
