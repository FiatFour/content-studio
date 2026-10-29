#!/usr/bin/env bash
# Manual, memory-guarded per-character SDXL LoRA training (decision D8).
# NEVER run from CI or `make test`. Procedure: docs/lora-training.md
#
# usage: CHAR=<slug> [TRIGGER_TOKEN=<token>] scripts/lora/train.sh [--check]
#   --check   validate inputs and print the plan, then exit without training
#
# Config (environment, defaults in parentheses):
#   SD_SCRIPTS_DIR    kohya-ss/sd-scripts checkout, pinned cb70b968 ($HOME/ai/sd-scripts)
#   SD_SCRIPTS_VENV   venv holding sd-scripts deps ($HOME/ai/sd-venv)
#   BASE_CHECKPOINT   SDXL checkpoint ($HOME/ai/ComfyUI/models/checkpoints/DreamShaperXL_Turbo_v2_1.safetensors)
#   DATASET_ROOT      holds <slug>/ with images + same-name .txt captions ($HOME/ai/lora-datasets)
#   OUT_DIR           trained LoRA, logs, staging ($HOME/ai/lora-out)
#   COMFYUI_LORA_DIR  ComfyUI loras folder; only used in the printed copy command ($HOME/ai/ComfyUI/models/loras)
#   STEPS             training steps (800; spike: 14 images x 5 repeats)
#   REPEATS           dataset repeats (5)
#   FORCE=1           overwrite an existing output LoRA
#   COMFY_PORT / OLLAMA_PORT   ports that must be free (8188 / 11434)
set -euo pipefail

die() { echo "lora-train: REFUSING: $*" >&2; exit 1; }
warn() { echo "lora-train: warning: $*" >&2; }
info() { echo "lora-train: $*"; }

CHECK_ONLY=0
case "${1:-}" in
  "") ;;
  --check) CHECK_ONLY=1 ;;
  *) die "unknown argument (only --check is accepted)" ;;
esac

SLUG="${CHAR:-}"
[[ "$SLUG" =~ ^[a-z0-9_-]{1,60}$ ]] || die "CHAR must match ^[a-z0-9_-]{1,60}$ (the character slug)"

TRIGGER_TOKEN="${TRIGGER_TOKEN:-$(printf '%s' "$SLUG" | tr -cd 'a-z0-9')chr}"
[[ "$TRIGGER_TOKEN" =~ ^[A-Za-z0-9]{3,40}$ ]] || die "TRIGGER_TOKEN must match ^[A-Za-z0-9]{3,40}$ (set it explicitly)"

SD_SCRIPTS_DIR="${SD_SCRIPTS_DIR:-$HOME/ai/sd-scripts}"
SD_SCRIPTS_VENV="${SD_SCRIPTS_VENV:-$HOME/ai/sd-venv}"
BASE_CHECKPOINT="${BASE_CHECKPOINT:-$HOME/ai/ComfyUI/models/checkpoints/DreamShaperXL_Turbo_v2_1.safetensors}"
DATASET_ROOT="${DATASET_ROOT:-$HOME/ai/lora-datasets}"
OUT_DIR="${OUT_DIR:-$HOME/ai/lora-out}"
COMFYUI_LORA_DIR="${COMFYUI_LORA_DIR:-$HOME/ai/ComfyUI/models/loras}"
STEPS="${STEPS:-800}"
REPEATS="${REPEATS:-5}"
COMFY_PORT="${COMFY_PORT:-8188}"
OLLAMA_PORT="${OLLAMA_PORT:-11434}"

[[ "$STEPS" =~ ^[0-9]{2,5}$ ]] || die "STEPS must be a number (10..99999)"
[[ "$REPEATS" =~ ^[0-9]{1,3}$ && "$REPEATS" -ge 1 ]] || die "REPEATS must be a positive number"
[[ "$COMFY_PORT" =~ ^[0-9]{1,5}$ && "$OLLAMA_PORT" =~ ^[0-9]{1,5}$ ]] || die "ports must be numeric"
for p in "$SD_SCRIPTS_DIR" "$SD_SCRIPTS_VENV" "$DATASET_ROOT" "$OUT_DIR" "$COMFYUI_LORA_DIR" "$BASE_CHECKPOINT"; do
  [[ "$p" == /* ]] || die "all paths must be absolute: $p"
done

DATASET_DIR="$DATASET_ROOT/$SLUG"
[[ -d "$DATASET_DIR" ]] || die "dataset dir does not exist: $DATASET_DIR"

# Dataset: >= 10 images, each with a non-empty caption that contains the trigger token.
shopt -s nullglob nocaseglob
images=("$DATASET_DIR"/*.png "$DATASET_DIR"/*.jpg "$DATASET_DIR"/*.jpeg "$DATASET_DIR"/*.webp)
shopt -u nullglob nocaseglob
[[ ${#images[@]} -ge 10 ]] || die "need >= 10 images in $DATASET_DIR, found ${#images[@]}"
for img in "${images[@]}"; do
  cap="${img%.*}.txt"
  [[ -f "$cap" ]] || die "missing caption file for $(basename "$img") (expected $(basename "$cap"))"
  [[ -s "$cap" ]] || die "empty caption file: $(basename "$cap")"
  grep -qwF -- "$TRIGGER_TOKEN" "$cap" || die "caption does not contain trigger token '$TRIGGER_TOKEN': $(basename "$cap")"
done

[[ -f "$SD_SCRIPTS_DIR/sdxl_train_network.py" ]] || die "sdxl_train_network.py not found in SD_SCRIPTS_DIR=$SD_SCRIPTS_DIR"
[[ -x "$SD_SCRIPTS_VENV/bin/python" ]] || die "no python in SD_SCRIPTS_VENV=$SD_SCRIPTS_VENV"
[[ -f "$BASE_CHECKPOINT" ]] || die "BASE_CHECKPOINT not found: $BASE_CHECKPOINT"

LORA_NAME="$SLUG.safetensors"
OUT_FILE="$OUT_DIR/$LORA_NAME"
if [[ -e "$OUT_FILE" && "${FORCE:-0}" != "1" ]]; then
  die "output exists: $OUT_FILE (set FORCE=1 to overwrite)"
fi

port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
if port_open "$COMFY_PORT"; then die "something is listening on 127.0.0.1:$COMFY_PORT (ComfyUI?). Stop it yourself; this script kills nothing."; fi
if port_open "$OLLAMA_PORT"; then die "something is listening on 127.0.0.1:$OLLAMA_PORT (Ollama?). Stop it yourself; this script kills nothing."; fi

command -v nvidia-smi >/dev/null || die "nvidia-smi not found; cannot verify the GPU is idle"
gpu_out="$(nvidia-smi --query-compute-apps=pid,used_memory --format=csv,noheader,nounits)" || die "nvidia-smi query failed"
while IFS=, read -r pid mem; do
  [[ -n "${pid// /}" ]] || continue
  mem="${mem// /}"
  if [[ "$mem" =~ ^[0-9]+$ && "$mem" -gt 1024 ]]; then
    die "GPU process $pid is using ${mem} MiB (> 1024). Free the GPU first."
  fi
done <<<"$gpu_out"

warn "Plug in AC power and disable sleep. This machine hard-resets (Kernel-Power 41) under GPU load and sleeps mid-job."

if [[ "${LORA_GUARDED:-0}" != "1" ]]; then
  command -v systemd-run >/dev/null || die "systemd-run not found; will not train without the memory guard"
  if [[ "$CHECK_ONLY" == "1" ]]; then
    info "check OK: ${#images[@]} images, trigger '$TRIGGER_TOKEN', output $OUT_FILE."
    info "training would re-exec under systemd-run --user --scope -p MemoryMax=9500M -p MemorySwapMax=3G. Nothing was run."
    exit 0
  fi
  info "re-executing under systemd-run (MemoryMax=9500M, MemorySwapMax=3G)"
  exec systemd-run --user --scope --quiet -p MemoryMax=9500M -p MemorySwapMax=3G \
    env LORA_GUARDED=1 TRIGGER_TOKEN="$TRIGGER_TOKEN" bash "$0"
fi
[[ "$CHECK_ONLY" == "0" ]] || die "internal: guarded run with --check"

# Guarded from here. Verify the cgroup limit is really in place.
cg="$(sed -n 's/^0:://p' /proc/self/cgroup)"
limit="$(cat "/sys/fs/cgroup${cg}/memory.max" 2>/dev/null || true)"
[[ "$limit" =~ ^[0-9]+$ && "$limit" -le $((9500 * 1024 * 1024)) ]] || die "memory limit not active in this cgroup (memory.max='${limit:-unreadable}')"

mkdir -p "$OUT_DIR/logs"
stamp="$(date +%Y%m%d-%H%M%S)"
LOG="$OUT_DIR/logs/$SLUG-$stamp.log"
STAGE="$OUT_DIR/staging/$SLUG"
STAGE_OUT="$OUT_DIR/staging/$SLUG-out"
rm -rf -- "$STAGE" "$STAGE_OUT"
mkdir -p "$STAGE/${REPEATS}_$SLUG" "$STAGE_OUT"
for img in "${images[@]}"; do
  ln -s "$img" "$STAGE/${REPEATS}_$SLUG/$(basename "$img")"
  ln -s "${img%.*}.txt" "$STAGE/${REPEATS}_$SLUG/$(basename "${img%.*}").txt"
done

info "training $SLUG: ${#images[@]} images x $REPEATS repeats, $STEPS steps. Log: $LOG (env is never logged)"
cd "$SD_SCRIPTS_DIR"
set +e
"$SD_SCRIPTS_VENV/bin/python" -m accelerate.commands.launch --num_cpu_threads_per_process 2 --mixed_precision bf16 sdxl_train_network.py \
  --pretrained_model_name_or_path "$BASE_CHECKPOINT" \
  --train_data_dir "$STAGE" --output_dir "$STAGE_OUT" --output_name "$SLUG" --caption_extension .txt \
  --resolution 640,640 --enable_bucket --min_bucket_reso 384 --max_bucket_reso 960 --bucket_reso_steps 64 \
  --network_module networks.lora --network_dim 8 --network_alpha 4 --network_train_unet_only \
  --train_batch_size 1 --max_train_steps "$STEPS" --gradient_checkpointing --cache_latents --sdpa \
  --mixed_precision bf16 --full_bf16 --save_precision fp16 --optimizer_type Adafactor \
  --optimizer_args scale_parameter=False relative_step=False warmup_init=False \
  --learning_rate 1e-4 --lr_scheduler constant --save_every_n_steps 400 --seed 42 --max_data_loader_n_workers 0 \
  >"$LOG" 2>&1
rc=$?
set -e
[[ $rc -eq 0 ]] || die "training failed (exit $rc; 137 usually means the memory guard killed it). See $LOG"
[[ -s "$STAGE_OUT/$LORA_NAME" ]] || die "training exited 0 but $STAGE_OUT/$LORA_NAME is missing. See $LOG"

mv -f -- "$STAGE_OUT/$LORA_NAME" "$OUT_FILE"
sha="$(sha256sum "$OUT_FILE" | cut -d' ' -f1)"

info "done: $OUT_FILE (sha256 $sha)"
echo
echo "Copy it to ComfyUI (not done automatically):"
printf '  cp -n -- %q %q\n' "$OUT_FILE" "$COMFYUI_LORA_DIR/$LORA_NAME"
echo
echo "Then on the Characters page for '$SLUG' enter:"
echo "  lora_name:     $LORA_NAME"
echo "  trigger_token: $TRIGGER_TOKEN"
echo
echo "Not verified: whether the character is recognizable. The owner judges that (docs/lora-training.md)."
