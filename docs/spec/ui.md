# UI

The reference design is the **"AI Content Studio UI"** design canvas (five screens): https://claude.ai/artifact/GsqQuFTroHf5pTcsdXQCHH
Its artboards are Dashboard, Episode production, Review & approval, Providers & budget (M2), and AI agent tower (M4). Match it, but real backend state always wins over the mockup.

## Rules
- Every control reads from and writes to a real API endpoint, with loading, success and error states. A button wired to a mock is not done.
- Nothing animates unless a real job is doing that work. When the backend is unreachable, show a clear "disconnected" state and freeze visuals rather than guess.
- Unimplemented features show "Not implemented / ยังไม่ได้สร้าง". Features from later milestones are visibly tagged (M2, M3) and inert.
- Accessible: real `<button>`, `<a>`, `<label>`; touch targets ≥ 44 px; text contrast ≥ 4.5:1; status never by colour alone (dot + word).
- Works at phone width (menus stack, wide tables scroll inside their box).

## Language (decision D13)
- Thai is the default; a **TH | EN** toggle sits in the top bar on every screen.
- Every UI string comes from a translation file (`th.json`, `en.json`); no literal copy in components. A test fails if a key exists in one language but not the other.
- The chosen language is saved per user in the backend.
- Stay in English in both languages: job states (`running`, `waiting_for_resource`…), task policies (`LOCAL_ONLY`…), hashes, log lines, model IDs.
- Thai font: IBM Plex Sans Thai. Check Thai line breaking in subtitles separately (that's the video, not the UI).

## Design tokens
| Token | Value | Use |
|---|---|---|
| `bg` | `#0B1424` | page background |
| `panel` | `#111D33` | cards |
| `panel-inner` | `#0E1A30` | nested boxes |
| `border` | `#233657` | card borders |
| `text` | `#EAF0FA` | primary text |
| `text-muted` | `#9DB0CC` | secondary text |
| `blue` | `#4DA3FF` | done / selected / links |
| `green` | `#34D399` | running / healthy / passed |
| `amber` | `#F5B942` | needs you / warning / primary action |
| `red` | `#FF8A7A` (on dark) · `#E5484D` (badge) | failed / destructive |
| `outline` | `#1B1B1B` | 2–4 px cartoon outline on tiles, avatars, primary buttons |

Fonts: Bricolage Grotesque (English display: logo, big numbers, agent names) · IBM Plex Sans Thai (all body text) · IBM Plex Mono (IDs, hashes, metrics).

Agent colours (used everywhere an agent appears):
Orchestrator `#8FB3D9`, Script `#F28DB2`, Art `#59C9B9`, Voice `#B49AF2`, Render `#F2C94C`, QA `#F29B5C`, Review (you) `#E9573F`, Guardrails `#9FBF8F`, Continuity `#E8D27A`.

## Shell (every screen)
- **Top bar:** logo + tagline · date/time · chips (episodes done today, CPU, RAM, VRAM, cloud spend this month) · TH/EN toggle · owner avatar ("Fiat · owner · approver").
- **Left menu:** Home · AI agent tower · Episodes · Needs your review (count badge) · Providers & budget (M2 tag) · Library (M3 tag, inert). Plus one context card (GPU slot, review queue or guardrails).

## Sign-in and Security (D9)
- Logged out, the app shows only a centred card with the TH | EN toggle: **enrollment** (one-time code from `go run ./cmd/server enroll`, passkey name) while no passkey exists, otherwise **Sign in with a passkey**. Failure messages are fixed and never say why (no hint whether a code or key was wrong). A 401 from any request returns the user here with "session ended". Open the app at `http://localhost:PORT` (passkeys need a domain name); the card says so when the page cannot run WebAuthn.
- The top bar has **Sign out**. Menu item **Security** lists passkeys (label, created, last used, "in use now"), adds one (needs the current session) and removes one with a two-step confirm; the last passkey has no remove button and says why. Language is kept in `localStorage` until sign-in, then saved in the backend as before.

## Screens
1. **Home / dashboard:** project banner, four summary tiles, active jobs table (real stage progress), this machine (VRAM/RAM/disk, GPU slot), waiting for review, recent exports, recent failures with "Retry stage".
2. **Episode production:** episode header and state, 8-stage strip (each stage tagged with its agent colour), 9:16 shot grid with real per-shot status, current-stage details (attempt, provider, model, measured time per shot, ETA computed from measurements), sanitized job log, artifacts so far, Cancel run.
3. **Review and approval:** version switcher, 9:16 player with subtitle preview, format/stream/duration/cost facts, QA list (pass / warning / AI advice; AI advice never blocks or approves), script and SRT tabs, the decision panel showing the exact version + sha256 with Approve / Request changes / Reject (notes required for the last two), version history.
4. **Providers and budget (M2):** budget tiles and threshold bar (70 / 90 / 100%), emergency stop, Discord notifications card (webhook status showing only the last 4 characters, event toggles, send test message, sample message, recent deliveries), providers table (separate *enabled* and *health* columns), task-policy table where every blocked route says why.
   Status: reachable at `#/providers` (the menu item is a real link now). Built so far: resource limits with a confirm step above the safe maximum (#41), a read-only task-policy table (#41), the Discord notifications card (#42). Budget and emergency stop show "Not implemented".
5. **AI agent tower (M4):** see below.

## Agent tower (M4)
A cutaway multi-storey building drawn in an original thick-outline cartoon style. No existing show characters, logos or signature props.
- **Floors:** B1 Guardrails (cost guard + vault; the Continuity snail sleeps here until M1b) · G Orchestrator lobby · 1F Script · 2F Art · 3F Voice + subtitles · 4F Render · 5F QA · 6F Review office (the owner's empty red chair) · Roof: story-core hologram and the export drone pad ("approved files only").
- **Episode crates** sit on the conveyor of the floor currently working on them.
- **Pipeline elevator** on the right: glass shaft, steel columns, roof motor room, a car parked at the active floor, and a floor sign at each landing with a status lamp (blue done, green active, grey ahead).
- **Progress tracker** beside the tower: blue check-dots for finished floors, a pulsing green dot for the active floor, hollow dots ahead, dashed line to the roof export.
- **Floor labels** are buttons showing the floor code, agent name, a one-line description and a live status dot. Clicking a label or a team-list row shows that agent's details (handoff from/to, current task, provider, last log line) in a card under the tower.
- **Right column:** today's episodes with progress, AI team list (tasks completed per agent; no made-up levels or points), and the clip waiting for review.
- **Pipeline panel** under the tower: the active episode's 8 stages. The last step is Export, never Publish.
- Status: `GET /api/tower` (read-only, session required) is built (#69). The static scene is built at `#/tower` (#70, `web/src/pages/Tower.tsx`, `TowerScene.tsx`, pure mapping in `tower.ts`): floors, elevator with the car parked at the active episode's floor (else the working/failed/waiting floor; with nothing running it rests at G and the page says so), floor signs and label buttons with a per-state glyph (tick, dot, ring, pause, cross, dash) plus the state word. Side panels are built (#71, `web/src/TowerPanels.tsx`, pure logic in `tower.ts`): progress tracker (check, pulsing, hollow dots; dashed line into the roof), agent detail card (missing values read "no data"), today's episodes with real stage progress, AI team list (null task count shown as a dash), the clip awaiting review linking to `#/review/<artifact id>`, and the 8-stage pipeline panel ending in Export. Limits: the pipeline panel shows one episode (see "Active episode" below); the team list shows the floor's state, not a level or score. State-driven motion is built (#72; pure `motionOf` and `pollInterval` in `tower.ts`, table-tested): a pulsing ring on the landing lamp and tracker dot of every `active` floor (G pulses while any run is live), episode crates on the conveyors of 1F-6F (a crate is drawn only for an episode the payload puts on that floor, and slides only while that floor is `active`), and the elevator car easing to the floor `carFloor` picks when it changes (parked at G, no motion, when nothing runs). Idle, done, failed, waiting and not_implemented floors never animate; failed keeps its static cross. Disconnected or error: all motion stops, crates and car stay where the last payload put them, the banner stays. `prefers-reduced-motion` removes all motion (state still reads from glyph, colour and word). Only CSS transform and opacity animate (moving parts are small HTML layers over the SVG so they run on the compositor; the car is an SVG transform and repaints only while it travels); no canvas, WebGL, requestAnimationFrame loop or new dependency; pulses and slides use stepped timing (about 5 and 10 frames per second) to keep CPU low. Animations pause when the tab is hidden (`visibilitychange`). Polling: 3 s while a run is active, 15 s when idle, 5 s while unreachable, none while hidden (one refetch when the tab shows again). Limits: a backend change shows within one poll interval (up to 15 s from idle); crates may overlap room props. Panels grey out and freeze when the backend is unreachable. On a failed or unreachable backend the scene freezes (greyed) on the last payload under a "disconnected" or error banner. The mapping lives in `internal/tower` (pure, table-tested) and is the only place floor states are decided. Animation tickets cite this table.

**Floors and what feeds them**

| Floor | Agent id | Fed by | Notes |
|---|---|---|---|
| B1 | `guardrails` | nothing | always `not_implemented`: cost guard and vault have no backend (D10, no cloud). The Continuity snail has no per-run data either; the response says so in `continuity.available = false`. |
| G | `orchestrator` | run state | `active` while any live run is `running`, else `idle`. It has no job of its own, so no task count, provider or handoff. |
| 1F / 2F / 3F / 4F / 5F | `script` / `art` / `voice` / `render` / `qa` | `jobs` | stages `script` / `images` / `tts`+`subtitles` / `render` / `qa`. |
| 6F | `review` | undecided QA-passed video | `waiting` while a latest video version has `qa_passed` and no approval row, else `idle`. Never `active`: the owner is the actor. |
| Roof | `export` | `exports` | `done` if an export row was made today, else `idle`. |

**Live runs.** Only the latest run of each episode counts, and only while it is `running`, `failed` (it needs the owner), or `ready_for_review` with a clip that has no decision yet. Cancelled runs and decided clips leave the floors. "Today" lists live runs plus any latest run created or touched since midnight (server time).

**Real job state to floor state** (`tower.FloorStateOf`; a floor looks at its stage jobs in every live run; first matching row wins):

| Rule | Floor state |
|---|---|
| any job `failed` | `failed` |
| any job `waiting_for_resource`, `waiting_for_human` or `retry_scheduled` | `waiting` |
| any job `running` | `active` |
| every planned stage of the floor, in every live run, has a `completed` job | `done` |
| anything else (only `queued`, `cancelled`, no job yet, a stage not reached) | `idle` |

Stage jobs are created one at a time as a run advances; a planned stage with no job yet therefore keeps its floor from being `done` (3F with `tts` completed and `subtitles` not reached is `idle`). A `queued` job is `idle`: nothing is working. An episode's crate stands on the floor of the first stage that has not completed, and on 6F when the run is complete and the clip awaits review.

**Other fields.** `tasks_completed` is the all-time count of `completed` jobs for the floor's stages (6F: approval rows, Roof: export rows; null for B1 and G, which record nothing). `provider`/`model` and `progress` come from the floor's most relevant job (failed, waiting, running, latest completed, queued, in that order) and are null when not recorded. `handoff_from` is the agent of the previous planned stage (orchestrator for the first); `handoff_to` is set only when that job completed. `last_log` is only a failed job's error message or a waiting job's wait reason, scrubbed of absolute paths and credential-looking text, one line, at most 160 characters. There is no job log table, so a healthy floor has no last line.
- Every pose, lamp, crate and pulse maps to a real job/agent-task state. Keep its GPU use light while local jobs run, and keep it optional: every core task works without this screen.

**Active episode (the pipeline panel).** `active_episode` is one episode with its 8-stage pipeline (`tower.activeCandidate`): first a running run that has a running job (the one that moved last), else a failed run or a running run with a job in `failed`, `waiting_for_resource`, `waiting_for_human` or `retry_scheduled` (the one that moved last). So a red or amber floor always has its pipeline, with the failed stage marked `failed` and later stages `pending`. A queued-only run, a cancelled run and a clip awaiting review have no pipeline; the panel then says nothing is running or needs attention. No stage state is invented: each comes from its job row, or `pending` when no job exists yet.

**Optional-ness (T5, #73).** The tower page is a lazy chunk (`React.lazy` in `App.tsx`; `pages/Tower.tsx`, the scene, the panels and `tower.css` ship as `Tower-*.js` and `Tower-*.css`). The shell and every other page never import it, load it or poll `/api/tower`; only the tower page polls. A failed chunk load or render is caught by a boundary around that page only (message plus Reload); a failing `/api/tower` shows the page's own error banner. Guarded by `web/src/route.test.ts` (static-import and stylesheet checks) and the browser checks below. Layout: the right column sits beside the scene only from the 2xl breakpoint (1536 px); below that it stacks under, so the elevator shaft stays inside the scene card.

**M4 acceptance status (2026-10-30).** Verified in headless Chromium against a throwaway database (verify-content-studio, seeded job rows, no worker, no GPU): states running, failed (pipeline shows `failed` tts), waiting (`waiting_for_resource`), awaiting review, idle, in TH and EN; disconnected (aborted `/api/tower`: banner, frozen greyed scene, zero infinite animations); reduced motion (zero infinite animations, versus 4 while a job runs); a 390 px viewport (no horizontal page scroll; the scene scrolls inside its card); keyboard-only (Tab reaches all floor and team buttons with a 3 px focus outline; Enter selects, Space toggles); `/api/tower` answering 500 and the Tower chunk blocked (other pages render, shell keeps polling `/api/system`, the tower chunk is not fetched until `#/tower` opens). Not verified: tab CPU or GPU cost on a real display (no measurement was taken, so "light" is unproven); behaviour with a real worker running a real job; Safari and Firefox. Known cosmetic limits: episode crates may overlap room props; at 390 px the scene is clipped and scrolls sideways inside its card; Thai pages show raw job state words (`running`, `completed`) because the existing `state.*` keys are English by design.

