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

## Screens
1. **Home / dashboard:** project banner, four summary tiles, active jobs table (real stage progress), this machine (VRAM/RAM/disk, GPU slot), waiting for review, recent exports, recent failures with "Retry stage".
2. **Episode production:** episode header and state, 8-stage strip (each stage tagged with its agent colour), 9:16 shot grid with real per-shot status, current-stage details (attempt, provider, model, measured time per shot, ETA computed from measurements), sanitized job log, artifacts so far, Cancel run.
3. **Review and approval:** version switcher, 9:16 player with subtitle preview, format/stream/duration/cost facts, QA list (pass / warning / AI advice; AI advice never blocks or approves), script and SRT tabs, the decision panel showing the exact version + sha256 with Approve / Request changes / Reject (notes required for the last two), version history.
4. **Providers and budget (M2):** budget tiles and threshold bar (70 / 90 / 100%), emergency stop, Discord notifications card (webhook status showing only the last 4 characters, event toggles, send test message, sample message, recent deliveries), providers table (separate *enabled* and *health* columns), task-policy table where every blocked route says why.
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
- Every pose, lamp, crate and pulse maps to a real job/agent-task state. Keep its GPU use light while local jobs run, and keep it optional: every core task works without this screen.
