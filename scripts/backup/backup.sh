#!/usr/bin/env bash
# Backs up the studio database, the artifact storage root and the (secret-free) config into
# one timestamped backup set, then applies retention (7 daily + 4 weekly). See docs/backup-restore.md.
#
# Environment (all optional):
#   STUDIO_DATABASE_URL   which database to dump (only its name is used; the password is never read out)
#   STUDIO_STORAGE_ROOT   artifact storage root (default var/storage)
#   STUDIO_BACKUP_ROOT    where backups go (default ~/studio-backups); must be outside the storage root
#   STUDIO_ENV_FILE       optional file of KEY=VALUE settings to include (filtered, never sourced)
#   STUDIO_PG_CONTAINER, STUDIO_PG_USER   Postgres container and user (studio-pg, studio)
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
umask 077

KEEP_DAILY=7
KEEP_WEEKLY=4

db_url=${STUDIO_DATABASE_URL:-$DEFAULT_DB_URL}
db_name=$(db_name_from_url "$db_url")
valid_db_name "$db_name" || die "database name '$db_name' is not allowed"
storage=$(resolve_path "${STUDIO_STORAGE_ROOT:-var/storage}")
backup_root=$(abspath "${STUDIO_BACKUP_ROOT:-${HOME:?set STUDIO_BACKUP_ROOT}/studio-backups}")

# Backups must live outside the app's write path (and the app must not live inside them).
is_within "$backup_root" "$storage" && die "backup root must not be inside the storage root"
is_within "$storage" "$backup_root" && die "storage root must not be inside the backup root"
is_within "$backup_root" "$REPO_ROOT" && die "backup root must be outside the repository"
[[ -d $storage ]] || die "storage root does not exist: $storage"
need_tools rsync date sort cp mv rm find sha256sum stat
need_pg

stamp=$(date -u +%Y%m%dT%H%M%SZ)
daily_dir=$backup_root/daily
weekly_dir=$backup_root/weekly
mkdir -p "$daily_dir" "$weekly_dir"
[[ ! -e $daily_dir/$stamp ]] || die "a backup set named $stamp already exists; wait a second and retry"
work=$daily_dir/.incomplete-$stamp
cleanup() { if [[ -n ${work:-} && -d $work ]]; then rm -rf -- "$work"; fi; return 0; }
trap cleanup EXIT
mkdir "$work" "$work/config"

# 1. Database first. A row is only written after its file exists, so syncing storage AFTER the dump
#    guarantees every file a dumped row points at is in the backup (extra newer files are harmless).
echo "dumping database $db_name"
pg pg_dump -U "$PG_USER" -d "$db_name" --format=custom --no-owner >"$work/db.dump"
[[ -s $work/db.dump ]] || die "pg_dump produced an empty file"
pg pg_restore --list <"$work/db.dump" >/dev/null || die "the dump does not read back with pg_restore --list"
migrations=$(psql_q "$db_name" "SELECT coalesce(string_agg(name, ',' ORDER BY name), '') FROM schema_migrations")
artifact_rows=$(psql_q "$db_name" "SELECT count(*) FROM artifacts")

# 2. Storage. Artifact files are immutable and never overwritten, so each set hard-links unchanged
#    files to the previous set: every set is complete, but only new files cost disk space.
echo "syncing storage $storage"
link=()
prev=$(list_sets "$daily_dir" | tail -n 1 || true)
if [[ -n $prev && -d $daily_dir/$prev/storage ]]; then link=(--link-dest="$daily_dir/$prev/storage"); fi
mkdir "$work/storage"
timeout 3600 rsync -a --exclude='.tmp-*' "${link[@]}" -- "$storage/" "$work/storage/"
storage_files=$(find "$work/storage" -type f | wc -l)

# 3. Config WITHOUT secrets. Decision rule, applied to every candidate setting:
#    a) only names on the allowlist below are ever read (the keys internal/config reads);
#    b) a name that looks like a secret (PASSWORD, SECRET, TOKEN, KEY, WEBHOOK, CREDENTIAL, AUTH) is skipped;
#    c) a value with credentials inside a URL (scheme://user:pass@host) is skipped;
#    d) STUDIO_DATABASE_URL is recorded as host and database only, never userinfo.
#    Skipped settings are listed BY NAME in config/skipped.txt. Values are never printed or logged.
allow=" STUDIO_ADDR STUDIO_STORAGE_ROOT STUDIO_EXPORT_DIR STUDIO_WEB_DIR OLLAMA_URL STUDIO_LLM_MODEL STUDIO_LLM_NUM_CTX
 COMFYUI_URL STUDIO_IMAGE_CHECKPOINT STUDIO_IMAGE_STEPS STUDIO_IMAGE_CFG STUDIO_LORA_STRENGTH STUDIO_IMAGE_SAMPLER
 STUDIO_IMAGE_SCHEDULER STUDIO_IMAGE_WIDTH STUDIO_IMAGE_HEIGHT THAIVOICE_URL FFMPEG_BIN FFPROBE_BIN STUDIO_FPS
 STUDIO_SUBTITLE_FONT STUDIO_MAX_ATTEMPTS STUDIO_SCRIPT_RETRIES "
allow=" $(echo $allow) "   # collapse newlines so every name is surrounded by single spaces
secret_name='PASSWORD|SECRET|TOKEN|KEY|WEBHOOK|CREDENTIAL|AUTH'
cred_url='://[^/@]*@'
declare -A cfg=()
if [[ -n ${STUDIO_ENV_FILE:-} ]]; then
  [[ -f $STUDIO_ENV_FILE ]] || die "STUDIO_ENV_FILE is not a file"
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line =~ ^[[:space:]]*([A-Z][A-Z0-9_]*)=(.*)$ ]] || continue
    v=${BASH_REMATCH[2]}
    v=${v#[\"\']}
    v=${v%[\"\']}
    cfg[${BASH_REMATCH[1]}]=$v
  done <"$STUDIO_ENV_FILE"
fi
for k in $allow; do
  if [[ -n ${!k:-} ]]; then cfg[$k]=${!k}; fi   # the real environment wins over the file
done
cfg[STUDIO_DATABASE_URL]=$db_url
: >"$work/config/studio.env"
: >"$work/config/skipped.txt"
for k in $(printf '%s\n' "${!cfg[@]}" | sort); do
  v=${cfg[$k]}
  if [[ $k == STUDIO_DATABASE_URL ]]; then
    hostpart=${v#*://}
    hostpart=${hostpart#*@}   # drop scheme and any userinfo
    printf 'STUDIO_DATABASE_URL_REDACTED=postgres://<credentials omitted>@%s\n' "${hostpart%%\?*}" >>"$work/config/studio.env"
    continue
  fi
  if [[ " $allow " != *" $k "* ]]; then echo "$k (not on allowlist)" >>"$work/config/skipped.txt"; continue; fi
  if [[ $k =~ $secret_name || $v =~ $cred_url ]]; then echo "$k (looks secret)" >>"$work/config/skipped.txt"; continue; fi
  printf '%s=%s\n' "$k" "$v" >>"$work/config/studio.env"
done

# 4. Manifest: facts a restore (and a human) can check without opening the dump.
{
  echo "created_utc=$stamp"
  echo "database=$db_name"
  echo "db_dump_sha256=$(sha256sum "$work/db.dump" | cut -d' ' -f1)"
  echo "db_dump_bytes=$(stat -c %s "$work/db.dump")"
  echo "artifact_rows=$artifact_rows"
  echo "storage_files=$storage_files"
  echo "migrations=$migrations"
} >"$work/MANIFEST.txt"

# 5. Publish the set atomically; a partial set never carries a stamp name.
mv -- "$work" "$daily_dir/$stamp"
work=""
echo "backup set: $daily_dir/$stamp"

# 6. Weekly set: the first daily set of each ISO week is also kept as a weekly set (hard links, no extra space).
week_of() { date -u -d "${1:0:4}-${1:4:2}-${1:6:2}" +%G-%V; }
this_week=$(week_of "$stamp")
have_week=0
for s in $(list_sets "$weekly_dir"); do
  if [[ $(week_of "$s") == "$this_week" ]]; then have_week=1; fi
done
if (( have_week == 0 )); then
  cp -al -- "$daily_dir/$stamp" "$weekly_dir/$stamp"
  echo "weekly set: $weekly_dir/$stamp"
fi

# 7. Retention, done here (no external scheduler needed). Only validated stamp directories are removed.
prune() {
  local dir=$1 keep=$2 s count
  count=$(list_sets "$dir" | wc -l)
  for s in $(list_sets "$dir"); do
    (( count > keep )) || break
    valid_stamp "$s" || continue
    rm -rf -- "${dir:?}/$s"
    count=$((count - 1))
    echo "pruned ${dir##*/}/$s"
  done
}
prune "$daily_dir" "$KEEP_DAILY"
prune "$weekly_dir" "$KEEP_WEEKLY"
echo "done"
