#!/usr/bin/env bash
# Stops the instance launch.sh started and drops its throwaway database. Safe to run twice and after
# a failed launch. Evidence under var/verify/<id>/evidence is kept. usage: cleanup.sh <env file>
set -euo pipefail
cd "$(dirname "$0")/../../../.."
. "$1"
# The env file is sourced as shell, so refuse anything that is not exactly what launch.sh writes.
[[ "$DB" =~ ^verify_[a-z0-9_]+$ ]] || { echo "refusing: database name '$DB' is not a verify_ throwaway" >&2; exit 1; }
[[ "$DIR" =~ ^var/verify/[0-9]+-[0-9]+$ ]] || { echo "refusing: directory '$DIR' is not a var/verify/<id> directory" >&2; exit 1; }
# Signal the pid only while it is still the server binary we built; a reused pid is left alone.
if [[ "$PID" =~ ^[0-9]+$ ]] && tr '\0' ' ' < "/proc/$PID/cmdline" 2>/dev/null | grep -q "^$DIR/server "; then
  kill "$PID" 2>/dev/null || true
  for _ in $(seq 20); do kill -0 "$PID" 2>/dev/null || break; sleep 0.25; done
fi
docker exec studio-pg psql -U studio -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)"
rm -rf "${DIR:?}/storage" "${DIR:?}/exports" "${DIR:?}/server"
echo "cleaned $ID; evidence kept in $DIR/evidence"
