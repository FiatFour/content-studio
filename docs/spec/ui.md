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
- Status: `GET /api/tower` (read-only, session required) is built (#69); the UI is not. The mapping lives in `internal/tower` (pure, table-tested) and is the only place floor states are decided. Animation tickets cite this table.

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
