#!/usr/bin/env bash
# Starts an ISOLATED studio API + web UI for verification: its own port, its own throwaway Postgres
# database, its own storage dirs, its own admin token. No worker and no AI models are started, so a
# created episode stays "queued" and no GPU is touched. The user's `make dev` stack is never used.
# Prints the path of the env file that doctor.sh, drive.mjs and cleanup.sh take as their argument.
set -euo pipefail
cd "$(dirname "$0")/../../../.."
export PATH="$HOME/.local/go/bin:$PATH"
docker exec studio-pg pg_isready -U studio -q || { echo "Postgres container studio-pg is not running (docker start studio-pg)" >&2; exit 1; }
ID=$(date +%s)-$$
DIR=var/verify/$ID; mkdir -p "$DIR/storage" "$DIR/exports"
DB=verify_$(echo "$ID" | tr -c 'a-z0-9\n' _)
PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
TOKEN=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')
docker exec studio-pg psql -U studio -q -c "CREATE DATABASE $DB"
[ -s web/dist/index.html ] || (cd web && npm run build >/dev/null)
go build -o "$DIR/server" ./cmd/server
STUDIO_ADDR=127.0.0.1:$PORT STUDIO_ADMIN_TOKEN=$TOKEN STUDIO_DATABASE_URL=postgres://studio:studio@127.0.0.1:5433/$DB \
  STUDIO_STORAGE_ROOT=$DIR/storage STUDIO_EXPORT_DIR=$DIR/exports setsid nohup "$DIR/server" >"$DIR/server.log" 2>&1 < /dev/null &
PID=$!
cat >"$DIR/env" <<ENV
ID=$ID
DIR=$DIR
DB=$DB
PORT=$PORT
PID=$PID
TOKEN=$TOKEN
URL=http://127.0.0.1:$PORT
ENV
chmod 600 "$DIR/env"
for _ in $(seq 40); do curl -sf -m 1 "http://127.0.0.1:$PORT/api/health" >/dev/null && { echo "$DIR/env"; exit 0; }; sleep 0.5; done
echo "server did not become healthy; see $DIR/server.log" >&2; kill "$PID" 2>/dev/null || true; exit 1
