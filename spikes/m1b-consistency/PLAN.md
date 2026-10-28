# M1b consistency spike: plan

Throwaway experiment (evidence, not application code). Decides D8. Spec: `docs/spec/milestones.md` (M1b step 1).
Status: **plan only; nothing installed or measured yet.**

## Question

Which technique keeps a character visually recognizable across shots on this machine (RTX 5060 Laptop, 8 GB VRAM, WSL2 with 11 GiB RAM), at what VRAM and time per image?

- **A. IP-Adapter with a reference sheet.** No training. Needs the ComfyUI node `cubiq/ComfyUI_IPAdapter_plus` (HEAD `a0f451a5`, third-party code), `ip-adapter-plus_sdxl_vit-h.safetensors` (848 MB) and the CLIP ViT-H image encoder (2.5 GB), both from `h94/IP-Adapter`.
- **B. Per-character LoRA.** Needs a training tool (`kohya-ss/sd-scripts`, HEAD `cb70b968`), a per-character image set, and a long sustained GPU load. Official docs say SDXL LoRA fits in 8 GB only with U-Net-only training, gradient checkpointing and a low rank (4 to 8); unverified on this machine.
- **C. Baseline.** Same prompts, no technique, to show what consistency costs and gains.

## Fixture

Two characters, six scenes each (different pose, place, expression and outfit-neutral), 12 images per arm, fixed seeds, the D4 checkpoint and sampler (DreamShaper XL Turbo v2.1, 8 steps, dpmpp_sde karras, cfg 2, 768×1344). Reference images for A come from the same checkpoint (one front view per character, chosen by the owner).

## Measurements

- Time per image (wall clock, warm model, median of the arm) and one-time cost (setup, dataset, training).
- Peak VRAM from `nvidia-smi` sampled each second during the arm.
- Quality: a contact sheet per arm for the **owner** to judge (recognizable / drifting / different person). That judgment is the result. A CLIP-embedding similarity to the reference may be reported as an advisory number only.

## Constraints

- One GPU-heavy job at a time. Stop `make dev` (the voice service holds about 4 GB) and unload Ollama before measuring; restart afterwards.
- AC power on, sleep off. Windows has hard-reset under GPU load here; LoRA training is the longest load, so it runs last and only after the owner confirms.
- Licenses of every weight recorded before download.
- Nothing from the spike enters `internal/`, the schema or `web/` until D8 is decided.

## Output

`spikes/m1b-consistency/RESULTS.md` with the table (arm, VRAM, time per image, one-time cost), contact sheets, owner verdict, recommendation. Then stop and wait for D8.
