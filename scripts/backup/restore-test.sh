#!/usr/bin/env bash
# Proves a backup is usable: restores it into a FRESH temporary database and a FRESH temporary
# storage directory, starts the real server binary against them on a spare localhost port, finds an
# approved episode, downloads its video through the API and checks that the sha256 equals the
# approved hash. Then it drops and deletes everything it created. Never touches the live database
# or storage, and never touches any other database.
#
# Usage:
#   restore-test.sh [--set <backup-set-dir>]   restore the newest daily set under STUDIO_BACKUP_ROOT
#   restore-test.sh --fixture                  build a throwaway source (approved fixture episode with a tiny
#                                              real video), back it up with backup.sh, then restore that
# Not part of `make test`. Needs Docker (studio-pg), rsync, curl, Go (to build the server).
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
export PATH="$HOME/.local/go/bin:$PATH"

mode=latest
set_dir=""
while (( $# )); do
  case $1 in
    --fixture) mode=fixture ;;
    --set) shift; (( $# )) || die "--set needs a directory"; set_dir=$(abspath "$1"); mode=set ;;
    *) die "unknown argument: $1 (usage: restore-test.sh [--set <dir> | --fixture])" ;;
  esac
  shift
done

need_tools curl sha256sum mktemp rsync go ss
need_pg

live_url=${STUDIO_DATABASE_URL:-$DEFAULT_DB_URL}
live_db=$(db_name_from_url "$live_url")
live_storage=$(resolve_path "${STUDIO_STORAGE_ROOT:-var/storage}")
live_addr=${STUDIO_ADDR:-127.0.0.1:8080}
# Same server and credentials as the live URL, different database name. The URL is only ever passed
# in the environment of child processes; it is never printed.
url_for_db() { local base=${live_url%%\?*}; printf '%s/%s' "${base%/*}" "$1"; }

work=$(mktemp -d "${TMPDIR:-/tmp}/studio-restore-test.XXXXXX")
[[ $work == /*/studio-restore-test.* ]] || die "unexpected temp dir"
src_db=""
restored_db=""
server_pid=""
ok=0
cleanup() {
  trap - EXIT
  if [[ -n $server_pid ]]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
  # Only databases this run created (names validated below) are ever dropped.
  local d
  for d in "$restored_db" "$src_db"; do
    if [[ -n $d && ( $d == studio_restore_* || $d == studio_rt_src_* ) && $d != "$live_db" ]]; then
      pg dropdb -U "$PG_USER" --if-exists --force "$d" >/dev/null 2>&1 || echo "warning: could not drop $d" >&2
    fi
  done
  if (( ok == 0 )) && [[ -s $work/server.log ]]; then echo "--- server log (last 20 lines) ---" >&2; tail -n 20 "$work/server.log" >&2; fi
  rm -rf -- "$work"
  if (( ok == 1 )); then echo "cleaned up temporary database and files"; fi
}
trap cleanup EXIT

suffix=$(date -u +%s)_$(head -c 4 /dev/urandom | od -An -tx1 | tr -d ' \n')
mkdir -p "$work/bin"

if [[ $mode == fixture ]]; then
  src_db=studio_rt_src_$suffix
  valid_db_name "$src_db" || die "bad generated name"
  [[ $src_db != "$live_db" ]] || die "refusing: source name equals live database"
  src_storage=$work/src-storage
  backup_root=$work/backups
  mkdir -p "$src_storage" "$backup_root"
  ffmpeg_bin=${FFMPEG_BIN:-/usr/bin/ffmpeg}
  ffprobe_bin=${FFPROBE_BIN:-/usr/bin/ffprobe}
  [[ -x $ffmpeg_bin && -x $ffprobe_bin ]] || die "ffmpeg/ffprobe not found (set FFMPEG_BIN, FFPROBE_BIN)"
  echo "fixture: creating source database $src_db and a 1 s test video"
  timeout 60 "$ffmpeg_bin" -v error -f lavfi -i color=c=black:s=72x128:r=10:d=1 -f lavfi -i anullsrc=r=16000:cl=mono \
    -t 1 -shortest -c:v libx264 -pix_fmt yuv420p -c:a aac "$work/fixture.mp4"
  timeout 30 "$ffprobe_bin" -v error -show_entries stream=codec_type -of csv=p=0 "$work/fixture.mp4" >/dev/null
  pg createdb -U "$PG_USER" "$src_db"
  go build -o "$work/bin/" ./scripts/backup/seedfixture
  seed_out=$(env STUDIO_DATABASE_URL="$(url_for_db "$src_db")" STUDIO_STORAGE_ROOT="$src_storage" FIXTURE_VIDEO="$work/fixture.mp4" "$work/bin/seedfixture")
  echo "fixture: $seed_out"
  env STUDIO_DATABASE_URL="$(url_for_db "$src_db")" STUDIO_STORAGE_ROOT="$src_storage" STUDIO_BACKUP_ROOT="$backup_root" "$(dirname "${BASH_SOURCE[0]}")/backup.sh"
  newest=$(list_sets "$backup_root/daily" | tail -n 1)
  [[ -n $newest ]] || die "backup.sh produced no set"
  set_dir=$backup_root/daily/$newest
elif [[ $mode == latest ]]; then
  backup_root=$(abspath "${STUDIO_BACKUP_ROOT:-${HOME:?set STUDIO_BACKUP_ROOT}/studio-backups}")
  newest=$(list_sets "$backup_root/daily" | tail -n 1)
  [[ -n $newest ]] || die "no backup sets under $backup_root/daily (run: make backup)"
  set_dir=$backup_root/daily/$newest
fi
[[ -d $set_dir ]] || die "backup set not found: $set_dir"
echo "backup set under test: $set_dir"

# Config in the set must carry no credentials.
if grep -rEq '://[^/@[:space:]]*:[^/@[:space:]]*@' "$set_dir/config"; then die "config backup contains a URL with credentials"; fi

restored_db=studio_restore_$suffix
restored_storage=$work/restored-storage
"$(dirname "${BASH_SOURCE[0]}")/restore.sh" "$set_dir" "$restored_db" "$restored_storage"
[[ $restored_db != "$live_db" && $restored_storage != "$live_storage" ]] || die "refusing: restore target is live"

# Spare localhost port: first free one in a fixed range, never the live app's port.
port=""
for p in $(seq 18080 18180); do
  [[ 127.0.0.1:$p == "$live_addr" ]] && continue
  if ! ss -ltn "sport = :$p" | grep -q LISTEN; then port=$p; break; fi
done
[[ -n $port ]] || die "no free port in 18080-18180"

go build -o "$work/bin/" ./cmd/server
mkdir -p "$work/exports" "$work/noweb"
env STUDIO_DATABASE_URL="$(url_for_db "$restored_db")" STUDIO_ADDR="127.0.0.1:$port" \
  STUDIO_STORAGE_ROOT="$restored_storage" STUDIO_EXPORT_DIR="$work/exports" STUDIO_WEB_DIR="$work/noweb" \
  "$work/bin/server" >"$work/server.log" 2>&1 &
server_pid=$!
base=http://127.0.0.1:$port/api
up=0
for _ in $(seq 1 60); do
  kill -0 "$server_pid" 2>/dev/null || die "server exited during startup"
  if curl -fsS --max-time 2 "$base/health" >/dev/null 2>&1; then up=1; break; fi
  sleep 0.5
done
(( up == 1 )) || die "server did not become healthy within 30 s"
echo "server up on 127.0.0.1:$port against $restored_db"

# The API needs a session. A throwaway session is minted straight into the RESTORED database by
# scripts/testsession, which refuses any database that is not a studio_restore_/verify_ throwaway on a
# loopback host (the live database "studio" can never match). The server has no auth bypass. The token
# goes to a mode-600 header file inside $work, never onto a command line or into the output.
go build -o "$work/bin/" ./scripts/testsession
( umask 077; printf 'Cookie: studio_session=%s\n' "$(env STUDIO_DATABASE_URL="$(url_for_db "$restored_db")" "$work/bin/testsession")" >"$work/auth.hdr" )
[[ -s $work/auth.hdr ]] || die "could not create a throwaway session in the restored database"
auth=(-H "@$work/auth.hdr")

# Newest video whose latest decision is 'approved'.
row=$(psql_q "$restored_db" "SELECT a.id, ap.sha256, a.sha256, a.episode_id FROM approvals ap JOIN artifacts a ON a.id = ap.artifact_id
  WHERE a.type = 'video' AND ap.decision = 'approved' AND ap.id = (SELECT max(id) FROM approvals WHERE artifact_id = a.id)
  ORDER BY ap.id DESC LIMIT 1")
[[ -n $row ]] || die "no approved episode in the restored database (nothing to verify)"
IFS='|' read -r art_id approved_sha row_sha episode_id <<<"$row"
valid_id "$art_id" && valid_id "$episode_id" || die "unexpected ids from database"
[[ $approved_sha =~ ^[0-9a-f]{64}$ ]] || die "approval has no valid sha256"
echo "approved episode id=$episode_id, video artifact id=$art_id, approved sha256=$approved_sha"

curl -fsS --max-time 30 "${auth[@]}" -o /dev/null "$base/videos/$art_id/review" || die "review endpoint failed for the restored artifact"
curl -fsS --max-time 600 "${auth[@]}" -o "$work/download.bin" "$base/artifacts/$art_id/content" || die "could not download the approved artifact through the API"
got=$(sha256sum "$work/download.bin" | cut -d' ' -f1)
echo "downloaded sha256=$got"
[[ $got == "$approved_sha" ]] || die "HASH MISMATCH: downloaded file differs from the approved hash"
[[ $row_sha == "$approved_sha" ]] || die "HASH MISMATCH: artifact row differs from the approved hash"
ffprobe_bin=${FFPROBE_BIN:-/usr/bin/ffprobe}
if [[ -x $ffprobe_bin ]]; then
  timeout 30 "$ffprobe_bin" -v error -show_entries format=duration -of csv=p=0 "$work/download.bin" >"$work/probe.txt" || die "ffprobe could not read the downloaded video"
  echo "ffprobe: duration $(tr -d '\n' <"$work/probe.txt") s"
fi

ok=1
echo "RESTORE TEST PASSED ($mode): restored set $(basename "$set_dir"), approved video hash verified through the running app"
