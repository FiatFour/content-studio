# M0 Assessment

Date: 2026-10-26. Evidence and rerun scripts: `spikes/m0-render/` (throwaway, not application code) and `docs/m0/*.png`.

## 1. Repo state
Greenfield. The directory was empty. M0 added `CLAUDE.md`, `docs/spec/`, this file, and the spike.

## 2. Facts and assumptions

### Confirmed (measured on this machine)
- **FFmpeg** 8.0.1 with libass, harfbuzz, fribidi, x264. `ffmpeg-skill doctor` passes; it warns 8.0.1 predates the CVE-2026-8461 fix (MagicYUV decoder, unused here). Run `apt upgrade ffmpeg`.
- **GPU** RTX 5060 Laptop is visible from WSL2: 8151 MiB total, 11 MiB used at idle.
- **RAM.** WSL2 sees **11.5 GiB** (MemTotal 12,095,152 kB), not 16 GB. There is no `.wslconfig`.
- **Go** was not installed. Go 1.27.3 is now in `~/.local/go` (user space). Postgres is not installed. Docker is available.
- **LLM (Ollama 0.34.2)**, schema-constrained JSON, Thai prompt, `num_ctx` 4096, 3 runs each:

  | Model | VRAM loaded | Gen speed | Wall per script | Valid JSON |
  |---|---|---|---|---|
  | `qwen3:8b` (Q4_K_M) | 5,490 MiB | 48.6 to 49.2 tok/s | 12 to 16 s | 3/3 (4/4 with probe rerun) |
  | `gemma4:12b` (Q4_K_M) | 6,682 MiB | 14.1 to 16.2 tok/s | 35 to 76 s | 3/3 |

  After `ollama stop`, VRAM returns to 11 MiB (measured). Every line came back in Thai.
- **ComfyUI** 0.42.0 is now in `~/ai/ComfyUI` (PyTorch 2.11 cu128, sm_120 supported). **TTS** venvs are in `~/ai/tts`. Results are below. **STT**: `openai-whisper` is installed via pipx (not faster-whisper).
- **Machine stability.** Windows hard-reset during the first SDXL run (Kernel-Power 41, BugcheckCode 0, i.e. no blue screen). The same event is logged on Oct 24 and Oct 25, before M0. Separately, the laptop went into Modern Standby mid-run (22:55 to 23:00), which stalled one image for 355 s. Cause of the resets is unknown. Power or thermal under load is a guess.
- **No C compiler** in WSL, so `torch.compile` fails. VoxCPM2 ran with `optimize=False`.
- **Thai fonts**: Noto Sans/Serif/Looped Thai are installed. IBM Plex Sans Thai is not, but the spec only needs it for the UI.

### Render spike (D11): same 60 s fixture, 6 stills, 1 WAV, 8 Thai cues, 1080×1920 at 30 fps
Both backends sit behind the same Go interface, `Render(ctx, Spec, work, out) error`. Run `spikes/m0-render/run.sh <dir>` to reproduce.

| | Plain FFmpeg from Go | ffmpeg-skill from Go (`insert`, `join`, `audio --replace`, `caption`) |
|---|---|---|
| Output | 1080×1920, 30 fps, h264+aac, 60.000 s, decodes | 1080×1920, 30 fps, h264+aac, 60.010 s, decodes |
| Wall time (3 runs, plus 1 rerun) | 19.4 / 19.8 / 22.2 / 25.2 s | 60.8 / 64.3 / 64.4 / 68.4 s |
| Peak RSS | 5.9 GB | 2.6 GB |
| Pan/zoom control | Any zoompan expression per shot. Timing comes from the frame count. | Presets only: zoom in/out, amount, pan in 4 directions. No easing or custom path. |
| Thai subtitles | Unwrapped Thai **runs off the frame** (`docs/m0/render-compare.png`). Inserting U+200B between words makes libass wrap at word boundaries (`docs/m0/thai-wrap.png`). | **Breaks words mid-word and splits them across cues in time**, e.g. `เนี่` / `ย` and `ค` / `รับ` in separate cues. The tool admits it: "sliced at the column edge". It also overrides our font size (13). |
| Error reporting | Raw ffmpeg stderr. Our own spec validation catches missing files before the run. | Structured JSON (`code`, `reason`, `retryable`). This is good. |
| Fit behind Go | One `exec` call and one filtergraph we own. | 4 Python processes, at least 3 video encode generations, a Python 3.10+ runtime dependency, and script-dir imports (breaks under `python -I`). |

Plain is about 3× faster because it encodes once; the skill path encodes per shot, in `join`, and in `caption` (inferred, not profiled).

**D11 decided: plain FFmpeg.** The skill's Thai re-wrapping breaks words. `check.py --content` stays a reference for warning-tier thresholds (it flagged -28 LUFS on the fixture).

### Image spike (D4): 4 shots per model, 768×1344, ComfyUI API (`spikes/m0-image/`, `docs/m0/image-compare.png`)

| | SDXL base 1.0 | DreamShaper XL Turbo v2.1 |
|---|---|---|
| License | CreativeML OpenRAIL++-M | CreativeML OpenRAIL++-M |
| Settings | 30 steps, dpmpp_2m karras, cfg 6.5 | 8 steps, dpmpp_sde karras, cfg 2 |
| Seconds per image (warm, n=3 / n=2 clean) | 33.1 / 33.8 | 21.1 / 21.4 / 22.1 |
| Cold first image | 43 s | 39 s |
| Peak VRAM (nvidia-smi) | 7,038 MiB | 7,038 MiB |
| Lowest free RAM during run | 1.9 GB, no swap | 1.9 GB, no swap |

Both hit the thick-outline cel look. DreamShaper's faces are more readable. Both failed the two-character shot (two grandmothers, no grandson). Same seed gives a byte-identical PNG. The sampler accounts for 18 s of DreamShaper's 21 s (log: 8/8 at about 1 to 2.3 s/it, 2 model evaluations per SDE step). I did not test native 1080×1920: SDXL is trained near 1 MP, so the plan is 768×1344 then Lanczos upscale (the render's zoompan already upscales 2×).
**Recommendation for D4: DreamShaper XL Turbo v2.1 at 768×1344.** A 15-shot episode is about 5.5 min of image time (estimate from the warm numbers).

### Voice spike (D5): 8 Thai lines, 2 characters (`spikes/m0-tts/`, samples in `~/ai/m0-samples/`)
Clarity is measured as Whisper turbo character error rate (CER) against the script. It is a proxy for intelligibility, not naturalness.

| | VoxCPM2 (openbmb) | MMS-TTS Thai (facebook) |
|---|---|---|
| License | **Apache-2.0** (code and weights) | CC-BY-NC 4.0 (non-commercial) |
| Voices | Designed from a text description, then cloned per character | One fixed voice |
| CER (2 runs) | 6.8% / 6.8% (deterministic) | 17.3% / 17.3% |
| Speed | about real time warm (RTF 1.0 to 1.5). First line 20 s | far faster than real time |
| VRAM | 5.3 GB loaded, 6.0 GB peak | 0.24 GB |

Notable VoxCPM2 errors: line 5 lost the opening "โอ๊ย!", and line 3 read ลูก as รูป. Line 8's 11% is mostly "สิบห้า" transcribed as "15". VoxCPM2 speaks faster than MMS (e.g. 3.2 s vs 5.7 s for line 8). Pace and naturalness need your ear.
Considered and excluded on license: ThonburianTTS (CC BY-NC-SA 4.0), OpenJaiTTS (CC BY-NC 4.0, gated), KhanomTan (CC-BY-NC-SA 3.0), OmniVoice (weights CC-BY-NC).
**Recommendation for D5: VoxCPM2**, with one designed reference clip per character stored as an artifact. It is the only commercial-use Thai option found.

### Assumptions (not verified)
- A Go Thai word segmenter (dictionary maximal-matching) is accurate enough for subtitle breaks. (Not tested. Only the ZWSP→libass step is verified.)
- Schema-constrained decoding guarantees syntax, not content: speaker names, line count, and length can still be wrong. With n=3 per model, "always valid" is not proven.
- The 16 GB host RAM can be raised for WSL via `.wslconfig`.

## 3. Proposed M1a schema and stages

Tables (key columns only):
- `projects` (id, name, profile, created_at)
- `episodes` (id, project_id, number, title, idea, target_duration_s, created_at)
- `workflow_runs` (id, episode_id, profile, state, created_at, finished_at)
- `jobs` (id, run_id, stage, seq, state, attempt_count, error_class, error_message, progress_done, progress_total, provider, model, started_at, finished_at, updated_at; unique (run_id, stage))
- `job_attempts` (id, job_id, n, started_at, finished_at, outcome, error_class, error_message)
- `artifacts` (id, project_id, run_id, job_id, type, version, storage_path, sha256, size_bytes, validation_status, provider, model, meta jsonb, license jsonb, created_at; unique (run_id, type, version))
- `artifact_parents` (artifact_id, parent_id)
- `qa_results` (id, artifact_id, rule, rule_version, severity [blocking|warning|advisory], passed, reason, suggested_fix, created_at)
- `approvals` (id, artifact_id, sha256, decision [approved|rejected|changes_requested], notes, reviewer, created_at). Append-only. The latest row for an artifact is its status.
- `exports` (id, approval_id, artifact_id, export_path, sha256, created_at)
- `user_settings` (user_id, ui_lang). D13 says language is stored in the backend.

The job state enum and transition table follow `jobs.md`, enforced in one Go function.

Stages, run sequentially by one worker:
`script → images → tts → subtitles → render → qa`. After QA passes, the run waits for review (`waiting_for_human`). **Export** is an explicit user action that re-hashes the file against the approval. It is not an automatic stage.
A stage counts as completed only after its artifact file is written (temp file → fsync → rename) and its row is committed in the same transaction as the job's `completed` transition.

## 4. Top 5 technical risks

1. **Machine stability.** Two unexplained Windows hard resets before M0 and one during it, plus sleep mid-job. *Mitigation:* run jobs on AC power with sleep disabled while the worker is busy, check GPU temperature under load, and rely on the M1a resume-or-fail checkpointing (risk 5). This makes the kill-mid-stage test essential, not optional.
2. **Memory headroom.** 8 GB VRAM: the LLM takes 5.5 GB, VoxCPM2 up to 6.0 GB, and SDXL up to 7.0 GB, so only one can be loaded at a time. 11.5 GiB WSL RAM: the plain render alone peaked at 5.9 GB. *Mitigation:* a GPU slot of 1, an explicit Ollama unload (verified back to 11 MiB) before ComfyUI, generating smaller and upscaling, rendering per shot into segments and concat-copying to cap RSS, and raising WSL memory in `.wslconfig`.
3. **Thai subtitle line breaking.** Measured: raw Thai overflows the frame, and the skill breaks words. *Mitigation:* a Go word segmenter inserts U+200B (libass wrapping verified), a QA rule flags any cue wider than the safe width, and the review page shows a subtitle contact sheet.
4. **Script fit: valid JSON, wrong content or length.** The ±10% duration gate depends on TTS durations that the script stage can't see. *Mitigation:* Go-side JSON Schema plus semantic checks (known speakers, line and character budget from the target length), bounded re-prompts, and a duration check right after TTS so a bad run fails before the expensive render.
5. **Crash consistency between files and rows.** A worker killed mid-write can leave orphan files or rows that point at partial files. *Mitigation:* write to a temp path and rename atomically, insert the artifact row in the same transaction as the job transition, start each stage by cleaning its own work dir on resume, and add a kill-mid-stage integration test against real Postgres (an M1a acceptance item).

## 5. Decisions blocking M1a

| # | Status | M0 input |
|---|---|---|
| D1 Content language | Decided 2026-10-26: Thai | Makes risk 1 the top unknown. |
| D2 Platform | Decided 2026-10-26: TikTok, FB Reels, IG Reels | One master; burned-in subs inside the safe zone all three share. Max lengths vs 120 s still to verify. |
| D3 LLM | Decided 2026-10-26: `qwen3:8b` via Ollama | Thai prose quality still to be judged on real episodes. |
| D4 Image stack | **Blocks**, recommend DreamShaper XL Turbo v2.1 | 21 s per image, 7.0 GB VRAM, OpenRAIL++-M. |
| D5 TTS | **Blocks**, recommend VoxCPM2 | Apache-2.0, 6.8% CER. Listen to the samples first. |
| D6 Music/SFX | Defaults to none | Not answered yet. M1a proceeds with none. |
| D7 Length | Decided 2026-10-26: 60 to 120 s | Per-episode target; QA ±10% of it. |
| D11 Render/QA | Decided 2026-10-26: plain FFmpeg | See section 2. |
