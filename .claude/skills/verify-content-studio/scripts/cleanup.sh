#!/usr/bin/env bash
# Stops the instance launch.sh started (by pid, never by name) and drops its throwaway database.
# Evidence under var/verify/<id>/evidence is kept. usage: cleanup.sh <env file>
set -euo pipefail
cd "$(dirname "$0")/../../../.."
. "$1"
kill "$PID" 2>/dev/null || true
for _ in $(seq 20); do kill -0 "$PID" 2>/dev/null || break; sleep 0.25; done
docker exec studio-pg psql -U studio -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)"
rm -rf "$DIR/storage" "$DIR/exports" "$DIR/server"
echo "cleaned $ID; evidence kept in $DIR/evidence"
