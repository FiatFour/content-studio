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
# Without a session the API must refuse (401); with the test session and an allowed Origin an empty
# body must reach validation (400). The Origin header is required on state-changing requests.
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "Origin: http://localhost:$PORT" "$URL/api/episodes" -d '{}')" = 401 ] || fail "POST /api/episodes without a session did not return 401"
if [ -n "${SESSION:-}" ]; then
  H=$(mktemp); trap 'rm -f "$H"' EXIT; (umask 077; printf 'Cookie: studio_session=%s\n' "$SESSION" >"$H")
  [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "Origin: http://localhost:$PORT" -H "@$H" "$URL/api/episodes" -d '{}')" = 400 ] || fail "POST /api/episodes with the test session and an empty body did not return 400"
fi
curl -sf "$URL/" | grep -q '<div id="root"' || fail "web UI is not being served (is web/dist built?)"
docker exec studio-pg psql -U studio -tAc "select 1 from pg_database where datname='$DB'" | grep -q 1 || fail "database $DB missing"
echo "doctor ok: $URL pid=$PID db=$DB"
