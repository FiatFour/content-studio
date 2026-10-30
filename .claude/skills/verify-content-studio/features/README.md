# AI Content Studio verification map

Maintained source for verifying the user-facing behavior of the studio web UI. Read this index before driving the app, then use the matching feature file.

## Baseline preconditions

- Postgres container `studio-pg` is running (`docker start studio-pg`).
- Browser tooling is set up once with `scripts/setup-browser.sh`.
- Launch an ISOLATED instance with `scripts/launch.sh`. It prints an env file path, called `$E` below. The instance has its own port, throwaway database and storage. It has no worker and no AI models, so it never touches the GPU and a new episode stays `queued`.
- Run `scripts/doctor.sh $E` and require `doctor ok` before driving.
- Never drive the user's own `make dev` stack on `127.0.0.1:8080`. It shares one database and one GPU.

## Driving conventions

- Run a flow with `node scripts/drive.mjs $E <flow>`. Exit code 0 means every check passed.
- Visible text comes from `web/src/i18n/{th,en}.json`, never hard-coded in the driver.
- Prefer ARIA roles and accessible names. The nav is `navigation "menu"`, the language switch is `group "Language"` with buttons `TH` and `EN`.
- Start each flow from the baseline. Flows set the language themselves through the API.
- Finish with `scripts/cleanup.sh $E`. It removes the instance and database and keeps `var/verify/<id>/evidence`.

## Proof and skip reporting

- Every flow writes `screenshot.png`, `aria.txt` and `result.json` to `var/verify/<id>/evidence/<flow>/`.
- UI proof pairs a visible result with a backend read (`GET /api/...`) of the stored state.
- Report an unreachable path with the attempted command and the unmet precondition. Do not report a skipped feature as verified through another path.

## Features

- [Shell and language](./shell-and-language.md): Thai default, TH/EN switch stored in the backend, inert "Not implemented" menu items. Flow `shell`.
- [Create an episode](./create-episode.md): form to episode page to queued first stage, with the stored row checked. Flow `create-episode`.
- [Passkey sign-in](./auth.md): enrollment, sign in/out, Security page, expired session, with a CDP virtual authenticator. Flow `auth` (needs a fresh instance).
- [Discord notifications](./notifications.md): Providers & budget screen, toggles, test message, masked suffix. Flow `notifications`.
- Resource limits and task policies (same screen `#/providers`): flow `resources` checks the confirm step above limit 1, persistence, audit rows and the read-only policy table.
- [Review, approve and export](./review-and-export.md): NOT yet driven by any flow; needs a video that passed QA.
- Knowledge Shorts sources (`#/episodes/<id>`, Sources card): flow `sources` seeds a knowledge_shorts project and episode by SQL (no UI path creates one yet), pastes a source with markup in it, checks it is shown as text, adds a version, and reads back the API and audit rows.
