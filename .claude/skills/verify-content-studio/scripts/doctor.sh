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
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$URL/api/episodes" -d '{}')" = 401 ] || fail "mutating endpoint did not demand the token"
[ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$URL/api/auth/check")" = 200 ] || fail "token not accepted"
curl -sf "$URL/" | grep -q '<div id="root"' || fail "web UI is not being served (is web/dist built?)"
docker exec studio-pg psql -U studio -tAc "select 1 from pg_database where datname='$DB'" | grep -q 1 || fail "database $DB missing"
echo "doctor ok: $URL pid=$PID db=$DB"
