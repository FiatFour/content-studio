# Open Decisions

Check this file before starting work. If an item marked **Blocks** for the current milestone is still open, stop and ask.
When the owner decides, record the answer and date here, and move the item to "Decided".
M0 measurements and recommendations for each open item are in `docs/assessment.md`.

## Open

| # | Decision | Blocks | Notes |
|---|---|---|---|

## Decided

| # | Decision | Answer | Date |
|---|---|---|---|
| D15 | **Knowledge Shorts sources** | The owner pastes source text; the app stores it as a versioned artifact. No web fetching, no web search, and no model-found facts. Every claim must cite a stored source and its quoted text must appear in that source (checked in code). | 2026-10-30 |
| D16 | **Knowledge Shorts length** | 30 to 60 s, set per profile. Fictional Series keeps 60 to 120 s (D7). | 2026-10-30 |
| D17 | **Knowledge Shorts visuals** | Stills from the existing ComfyUI stack in a flat infographic style, no character LoRA. Voice, subtitles and render reuse the existing stages. | 2026-10-30 |
| D10 | **First cloud provider** | None. No cloud provider will be used; the app stays local-only. Budget, kill switch, cloud adapter and provider error mapping (issues #19-#21, #40) are closed as not planned. Reopen this decision before writing any cloud code; verify current pricing and commercial-use terms from official docs first. | 2026-10-30 |
| D9 | **Auth approach** | Passkey (WebAuthn) with a server-side session cookie; one owner, no user management. First passkey only through a one-time terminal code (`server enroll`), later ones only from a signed-in session. Implemented with `github.com/go-webauthn/webauthn`. Details and the endpoint matrix: `security-ops.md`. | 2026-10-29 |
| D8 | **Character consistency technique** | Per-character LoRA (option B of the M1b spike). Evidence: `spikes/m1b-consistency/RESULTS.md`. Costs to plan for: about 15 min of exclusive GPU per character (7.6 GB VRAM), a curated image set per character (the spike's set came from IP-Adapter and biased pose), and training only behind a memory limit (`systemd-run` `MemoryMax`) because an unguarded run crashed WSL. A LoRA does not fix style drift or the photographic look. | 2026-10-29 |
| D12 | **Notification channel** | Discord, via outbound channel webhooks only (no bot, no interactions). See `notifications.md`. | 2026-10-26 |
| D14 | **UI design direction** | Navy cartoon UI with thick-outlined tiles; Thai-first; agent tower visualization for M4. See `ui.md` and the "AI Content Studio UI" design canvas. | 2026-10-26 |
| D13 | **UI language** | Thai and English, switchable from a TH/EN toggle in the top bar; Thai is the default. The choice is saved in the backend per user, not only in the browser. Technical identifiers (policies, states, hashes, logs) stay in English. | 2026-10-26 |
| D1 | **Content language** | Thai (dialogue, subtitles, script prompts). | 2026-10-26 |
| D2 | **Target platforms** | TikTok, Facebook Reels, Instagram Reels. One 1080×1920 master serves all three. Subtitles burned in, kept inside the safe zone shared by all three apps' on-screen UI. Check each platform's current max length against the 120 s upper bound before M1a QA rules are written. | 2026-10-26 |
| D3 | **Local LLM** | Ollama with `qwen3:8b` (Q4_K_M), schema-constrained JSON. Measured 5.5 GB VRAM, ~49 tok/s (`docs/assessment.md`). | 2026-10-26 |
| D7 | **Episode length** | 60 to 120 s. Each episode sets its own target in that range; QA checks ±10% of that target. | 2026-10-26 |
| D11 | **Render/QA backend** | Plain FFmpeg/ffprobe from Go. ffmpeg-skill stays a dev-time helper only. Reason: it breaks Thai words in subtitles (M0 spike). | 2026-10-26 |
| D4 | **Image stack** | ComfyUI with DreamShaper XL Turbo v2.1 (CreativeML OpenRAIL++-M), 8 steps, dpmpp_sde karras, cfg 2, 768×1344 then upscaled in the render. Measured ~21 s/image, 7.0 GB VRAM. | 2026-10-26 |
| D5 | **Local TTS** | VoxCPM2 (Apache-2.0 code and weights), one designed reference voice per character, stored as an artifact. Measured 6.8% Whisper CER, about real time, 6.0 GB VRAM peak. | 2026-10-26 |
| D6 | **Music/SFX** | None for v1 (spec default; owner did not choose otherwise). | 2026-10-26 |
