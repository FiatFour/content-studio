# Per-character LoRA training (M1b, decision D8)

Manual procedure. The app never trains anything. Evidence and numbers come from `spikes/m1b-consistency/RESULTS.md` (Arm B).

**Status: `scripts/lora/train.sh` has never been run end to end.** It was reconstructed from the spike's `train_lora.sh` (outside git) and only the refusal paths were exercised (bad slug, missing dataset, too few images, caption without trigger token). The memory guard re-exec, the cgroup check, the real training call, and the final copy/UI instructions are unverified until the owner runs it once.

## Cost, and why you must plan for it

| | Measured in the spike |
|---|---|
| Time | about 15 min per character (800 steps, 1.06 s/step) |
| GPU | exclusive; peak 7588 MiB of 8 GB, nothing else may use it |
| RAM | about 10.0 GB including swap (WSL sees 11.5 GiB) |

Do not run it while ComfyUI, Ollama, or anything else holds the GPU. The script refuses if ports 8188/11434 answer or if a GPU process uses more than 1 GB. It never kills anything; stop those yourself.

## Why the memory guard exists

The first spike attempt loaded SDXL in fp32, RAM hit 11.78 of 11.8 GB and the whole WSL VM crashed. The working setup is `--full_bf16`, 3 GB swap, and the process running under `systemd-run --user --scope -p MemoryMax=9500M -p MemorySwapMax=3G`, so an out-of-memory event kills only the training process. The script re-execs itself under that limit, checks the cgroup `memory.max` actually took effect, and refuses to train without it. Exit 137 in the log message usually means the guard fired. Needs a working `systemd --user` session in WSL.

## Power and sleep

This machine hard-resets under GPU load (Kernel-Power 41, no bugcheck) and the laptop also sleeps mid-job. Before starting: plug in AC power, disable sleep, close other GPU work. The script only prints a warning; it cannot check this for you.

## Prepare the dataset (one folder per character)

```
$DATASET_ROOT/<slug>/            default DATASET_ROOT=$HOME/ai/lora-datasets
    001.png   001.txt
    002.png   002.txt
    ...
```

- `<slug>` is the character slug, `^[a-z0-9_-]{1,60}$`.
- At least 10 images (png/jpg/jpeg/webp); the spike used 14. Every image needs a same-name `.txt` caption.
- Pick a trigger token, `^[A-Za-z0-9]{3,40}$`, a made-up word (for example `grmaxy`), not a real word. Every caption must contain it, for example: `grmaxy, an elderly woman with round glasses, sarong, standing in a kitchen`. The script rejects captions without it.
- Caption what varies (pose, framing, setting, expression), not the identity features you want the token to absorb.
- Curate by hand. **Warning: the spike dataset was generated with IP-Adapter from one reference, so the LoRA learned that single front-facing, hands-on-hips pose and every scene looked like a pasted figure.** Use varied poses, angles (front, three-quarter, profile), framing (close-up to full body), expressions, and backgrounds. Pose variety in the dataset is what gives pose variety in episodes.
- Original characters only (see `docs/spec/product.md`).

## Run

```
make lora-train-check CHAR=<slug> TRIGGER_TOKEN=<token>   # validates inputs, trains nothing
make lora-train       CHAR=<slug> TRIGGER_TOKEN=<token>   # manual only, never in test/CI
```

If `TRIGGER_TOKEN` is omitted it defaults to the slug's letters/digits plus `chr`; set it explicitly instead. `make lora-train-check` also checks ports and queries `nvidia-smi` (read-only); it never starts training.

Settings (hard-coded to the spike): SDXL U-Net-only LoRA, rank 8 / alpha 4, 640 px with bucketing, batch 1, gradient checkpointing, bf16 `--full_bf16`, Adafactor lr 1e-4, 800 steps (`STEPS`), 5 repeats (`REPEATS`), kohya-ss/sd-scripts `cb70b968`. The exact flags were copied from the spike script; adjust `STEPS` for datasets much larger or smaller than 14 images.

Environment variables and defaults:

| Variable | Default |
|---|---|
| `SD_SCRIPTS_DIR` | `$HOME/ai/sd-scripts` |
| `SD_SCRIPTS_VENV` | `$HOME/ai/sd-venv` |
| `BASE_CHECKPOINT` | `$HOME/ai/ComfyUI/models/checkpoints/DreamShaperXL_Turbo_v2_1.safetensors` (D4; the LoRA only fits the base it was trained on) |
| `DATASET_ROOT` | `$HOME/ai/lora-datasets` |
| `OUT_DIR` | `$HOME/ai/lora-out` |
| `COMFYUI_LORA_DIR` | `$HOME/ai/ComfyUI/models/loras` (only used in the printed copy command) |
| `FORCE` | unset; set `FORCE=1` to overwrite an existing `$OUT_DIR/<slug>.safetensors` |

Output goes to `$OUT_DIR/<slug>.safetensors`; the log to `$OUT_DIR/logs/<slug>-<timestamp>.log` (the script never prints or logs the environment). Follow progress with `tail -f` on that log from another terminal.

## After training

The script prints, and does not run, the copy command, for example:

```
cp -n -- $OUT_DIR/<slug>.safetensors $COMFYUI_LORA_DIR/<slug>.safetensors
```

Then open the Characters page, edit the character, and set:

- `lora_name`: the file name in the ComfyUI loras folder, `<slug>.safetensors` (letters, digits, `_ . -` only).
- `trigger_token`: the token used in the captions.

Once the image-stage PR is merged, the image stage loads `lora_name` into the workflow and adds `trigger_token` to the prompt for that character. Until then, setting these fields stores them but they have no effect on generated images. As of this writing (origin/main at 7940cdd) that PR is not merged: the API validates and stores `lora_name` and `trigger_token`, but nothing in the image stage reads them yet.

## Acceptance is the owner's call

This tooling checks inputs and that training exited cleanly and produced a file. It does not and cannot judge quality. The LoRA is accepted when the owner finds the character recognizable across three consecutive episodes. The spike found the LoRA does not fix rendering style drift (outputs stayed photographic rather than adult-animated), so do not expect it to.

## Not verified

- The script has never been run end to end on this machine (no training was run when it was written).
- The `systemd-run` re-exec, cgroup limit check, and `accelerate` invocation are untested in the script form.
- Peak VRAM/RAM and timing for datasets other than the spike's 14 images.
- Whether a curated dataset gives better pose variety than the IP-Adapter-generated one (listed as "not done" in the spike results).
