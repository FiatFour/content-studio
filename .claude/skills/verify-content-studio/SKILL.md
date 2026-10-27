---
name: verify-content-studio
description: Drive the AI Content Studio web UI (React + Go API) in headless Chromium against an isolated throwaway instance and capture screenshot, ARIA and backend-state evidence. Use to prove a UI or API change works for a user; not for real-model pipeline runs.
---

# Verify AI Content Studio

You write evidence for a reader who has not seen this app. Run these steps from the repo root. All scripts live in `.claude/skills/verify-content-studio/scripts/` (called `$SK/scripts` below).

## Surface

Primary: the web UI (hash routes `#/`, `#/episodes`, `#/episodes/<id>`, `#/review`, `#/review/<id>`) over the Go API on `/api`. The worker and the local AI models (Ollama, ComfyUI, voice service) are a separate live stack and are NOT part of this skill.

## Launch

1. One-time: `$SK/scripts/setup-browser.sh`. It installs Playwright 1.63 and Chromium into `var/verify/tools` and unpacks `libnss3`/`libnspr4` without sudo. Playwright older than 1.63 has no build for this Ubuntu.
2. `E=$($SK/scripts/launch.sh)`. It creates database `verify_<id>` in the `studio-pg` container, builds and starts `server` on a free loopback port with its own token and storage, and prints the env file path. Ready means `/api/health` is ok. Migrations ran on start.

The instance has no worker and no models, so it uses no GPU and an episode stays `queued`. Two instances can run side by side (own port, own database). Never drive `127.0.0.1:8080`: that is the user's live stack with real data and a GPU job possibly running.

## Doctor

`$SK/scripts/doctor.sh $E` is read-only. It checks the pid is alive and owns the port, health is ok, a mutation without a token gets 401, the token works, the UI is served, and the database exists. Run it first, and again whenever a flow behaves oddly.

## Drive

`node $SK/scripts/drive.mjs $E <flow>` with flow `shell`, `token` or `create-episode`. Exit 0 means all checks passed. Feature recipes and the handles they use are in `$SK/features/README.md`. Add a flow to `drive.mjs` and a feature file together.

## Evidence

Each flow writes `var/verify/<id>/evidence/<flow>/{screenshot.png,aria.txt,result.json}`. A proof must:

- use the real user path (typing into labelled fields and pressing buttons), never internal setters;
- show the action and the resulting state, not just the last screen;
- pair what is visible with a backend read (`GET /api/...`) of what was stored;
- read the screenshot, not only the exit code.

## Cleanup

`$SK/scripts/cleanup.sh $E` stops the server by pid, drops the database and removes its storage. Evidence in `var/verify/<id>/evidence` is kept. Run it after failed attempts too.

## Not covered

Anything that needs the worker or models (script, images, voice, render, QA) and the review/approve/export UI, which needs a QA-passed video. See `features/review-and-export.md`.
