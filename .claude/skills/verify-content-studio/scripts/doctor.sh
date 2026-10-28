#!/usr/bin/env bash
# Read-only: is this instance the isolated one we started, healthy, and worth driving?
# usage: doctor.sh <env file>
set -euo pipefail
cd "$(dirname "$0")/../../../.."
. "$1"
fail() { echo "DOCTOR FAIL: $*" >&2; exit 1; }
kill -0 "$PID" 2>/dev/null || fail "server pid $PID is not running"
ss -ltnp 2>/dev/null | grep -q "127.0.0.1:$PORT .*pid=$PID," || fail "port $PORT is not owned by pid $PID"
[ "$(curl -sf "$URL/api/health")" = '{"status":"ok"}' ] || fail "/api/health is not ok"
# There is no auth in M1a, so an empty body must reach validation (400), not an auth wall (401).
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' "$URL/api/episodes" -d '{}')" = 400 ] || fail "POST /api/episodes with an empty body did not return 400"
curl -sf "$URL/" | grep -q '<div id="root"' || fail "web UI is not being served (is web/dist built?)"
docker exec studio-pg psql -U studio -tAc "select 1 from pg_database where datname='$DB'" | grep -q 1 || fail "database $DB missing"
echo "doctor ok: $URL pid=$PID db=$DB"
