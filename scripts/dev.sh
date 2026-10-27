#!/usr/bin/env bash
# Starts every local service for development and stops them together on Ctrl-C.
# Paths of the AI runtimes installed during M0 can be overridden with the env vars below.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$HOME/.local/go/bin:$PATH"
COMFY_DIR=${COMFY_DIR:-$HOME/ai/ComfyUI}
VOICE_PY=${VOICE_PY:-$HOME/ai/tts/voxcpm/bin/python}
mkdir -p var/logs var/storage var/exports

if [ ! -s var/admin-token ]; then
  (umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=' > var/admin-token)
fi
export STUDIO_ADMIN_TOKEN=$(cat var/admin-token)

if ! docker ps --format '{{.Names}}' | grep -qx studio-pg; then
  docker start studio-pg >/dev/null 2>&1 || docker run -d --name studio-pg -e POSTGRES_USER=studio -e POSTGRES_PASSWORD=studio \
    -e POSTGRES_DB=studio -p 127.0.0.1:5433:5432 -v studio-pgdata:/var/lib/postgresql/data postgres:17 >/dev/null
fi
until docker exec studio-pg pg_isready -U studio -q; do sleep 1; done

pids=()
cleanup() { trap - EXIT INT TERM; for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; wait 2>/dev/null || true; }
trap cleanup EXIT INT TERM

start() { local name=$1; shift; "$@" >"var/logs/$name.log" 2>&1 & pids+=($!); echo "started $name (pid $!)"; }

curl -s -m 2 127.0.0.1:11434/api/tags >/dev/null || { echo "Ollama is not running on 127.0.0.1:11434; start it first" >&2; exit 1; }
start comfyui bash -c "cd '$COMFY_DIR' && exec .venv/bin/python main.py --listen 127.0.0.1 --port 8188"
start thaivoice "$VOICE_PY" services/thaivoice/server.py
(cd web && npm run build >/dev/null)
go build -o bin/ ./cmd/...
start server bin/server
start worker bin/worker

echo
echo "Studio:  http://127.0.0.1:8080"
echo "Admin token (paste it into the top bar): $(cat var/admin-token)"
echo "Logs in var/logs. Keep the laptop plugged in with sleep off while jobs run."
wait
