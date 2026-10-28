# M1b consistency spike: how to run

Spec: `PLAN.md`. Everything here is throwaway evidence. Nothing has been run against the real GPU yet; the harness was tested only against a fake server (`tests/run_tests.sh`).

## Before running (controller checklist)

1. Stop `make dev` (voice service holds ~4 GB) and unload Ollama; AC power on, sleep off. One GPU job at a time.
2. GPU idle: `nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits` should be near the desktop baseline. The harness records this baseline as `baseline_vram_mib`.
3. ComfyUI up: `curl -s 127.0.0.1:8188/system_stats | head -c 200`.
4. Files present (checked at write time): `~/ai/ComfyUI/models/checkpoints/DreamShaperXL_Turbo_v2_1.safetensors`, `models/ipadapter/ip-adapter-plus_sdxl_vit-h.safetensors`, `models/clip_vision/CLIP-ViT-H-14-laion2B-s32B-b79K.safetensors`; and `GET /object_info/IPAdapterAdvanced` answers.
5. Workflows still valid against the live server: `python3 validate_workflows.py` (offline, against the saved `object_info.json`). To re-check against the running ComfyUI: `curl -s 127.0.0.1:8188/object_info > /tmp/oi.json && python3 validate_workflows.py /tmp/oi.json`.
6. Offline tests of the harness: `sh tests/run_tests.sh` (expects `ALL PASS`).

## Run (from this folder; use a fresh `--out` each time)

```
OUT=~/ai/m1b-out   # outside git; images are generated media
python3 harness.py --arm REF --workflow workflows/reference_sheet.json --out $OUT/ref
```
Produces `ref_yai_nuan.png`, `ref_lung_chai.png`. **Owner picks the reference** (to try other candidates, change `reference.seed_base` in a copy of the fixture and pass `--fixture`). Copy the chosen images as `$OUT/refs/yai_nuan.png` and `$OUT/refs/lung_chai.png`.

```
python3 harness.py --arm C --workflow workflows/arm_c_baseline.json --out $OUT/c
python3 harness.py --arm A --workflow workflows/arm_a_ipadapter.json --out $OUT/a --refs-dir $OUT/refs   # --weight 0.7 default
~/ai/ComfyUI/.venv/bin/python contact_sheet.py $OUT/c
~/ai/ComfyUI/.venv/bin/python contact_sheet.py $OUT/a --refs-dir $OUT/refs
```
Pillow exists only in the ComfyUI venv (system python has none). Contact sheets land in `<out>/contact_<arm>.png`.

Expected: each arm prints one line per image and ends with `wrote .../results.json: 12 images, cold=... warm_median=... peak_vram=...MiB`; exit code 0. Arm C and A are 12 images, REF is 2. Run the REF arm first (it also loads the checkpoint), then C, then A; order does not matter for results since every arm reports its own cold image. Arm A uploads the reference images through `POST /upload/image` (writes into ComfyUI's `input/` dir, no prompt, no GPU).

`results.json` fields: `arm, settings, ipadapter_weight, baseline_vram_mib, peak_vram_mib, vram_samples, vram_sample_failures, cold{seconds,file}, warm_s[], warm_median_s, images[{char,scene,seed,prompt_id,seconds,cold,file}], error, wall_total_s`. On any failure the arm stops, partial results are written, `error` is set, exit code 1. Never overwrites an existing `results.json`.

## Notes and limits

- Seconds per image = `POST /prompt` until `/history` reports success (polled every 0.2 s); download time is excluded. The first image of an arm is cold and excluded from the warm median (11 warm values per 12-image arm).
- VRAM is sampled at 1 Hz, so peaks shorter than a second can be missed; it is whole-GPU `memory.used`, including the desktop and any other process, hence the recorded baseline.
- Arm A and C use the identical prompt text (style + appearance + scene) and seeds, so only the technique differs. The IP-Adapter node is `IPAdapterAdvanced` (weight 0.7, `linear`, `concat`, `V only`, 0.0 to 1.0); the plain `IPAdapter` node has no `clip_vision` input on this install, so `CLIPVisionLoader` is wired into the Advanced node.
- Arm B (LoRA) is not covered here.
- Quality is judged by the owner from the contact sheets; no similarity score is computed.
