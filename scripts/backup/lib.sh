# Shared helpers for backup.sh, restore.sh and restore-test.sh. Sourced, not executed.
# Postgres runs in the studio-pg container (see `make db`), so pg_dump, pg_restore and psql are
# run with `docker exec`. No password is needed: the container's local socket trusts its own user.
# Nothing here ever prints environment values.

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

PG_CONTAINER=${STUDIO_PG_CONTAINER:-studio-pg}
PG_USER=${STUDIO_PG_USER:-studio}
DEFAULT_DB_URL='postgres://studio:studio@127.0.0.1:5433/studio'

die() { echo "error: $*" >&2; exit 1; }

# Names are used in SQL identifiers and shell arguments, so only a strict charset is accepted.
valid_db_name() { [[ $1 =~ ^[a-z][a-z0-9_]{0,62}$ ]]; }
valid_stamp() { [[ $1 =~ ^[0-9]{8}T[0-9]{6}Z$ ]]; }
valid_id() { [[ $1 =~ ^[0-9]{1,18}$ ]]; }

# db_name_from_url <url>: prints the database name; the rest of the URL (which holds the password) is dropped.
db_name_from_url() {
  local re='^postgres(ql)?://[^/]*/([A-Za-z0-9_]+)(\?.*)?$'
  [[ $1 =~ $re ]] || die "STUDIO_DATABASE_URL is not a postgres:// URL with a database name"
  printf '%s' "${BASH_REMATCH[2]}"
}

# Resolve a path without requiring it to exist (also normalizes .. and symlinks that exist).
abspath() { realpath -m -- "$1"; }

# is_within <child> <parent>: true when child equals parent or lies below it (both absolute).
is_within() { [[ $1 == "$2" || $1 == "$2"/* ]]; }

# Prints an absolute path; relative paths are relative to the repo root, as the app's are
# relative to the directory it runs in (the repo root under `make dev`).
resolve_path() { case $1 in /*) abspath "$1" ;; *) abspath "$REPO_ROOT/$1" ;; esac; }

pg() { timeout "${PG_TIMEOUT:-900}" docker exec -i "$PG_CONTAINER" "$@"; }
psql_q() { local db=$1 sql=$2; pg psql -U "$PG_USER" -d "$db" -v ON_ERROR_STOP=1 -tAX -c "$sql"; }

need_tools() { local t; for t in "$@"; do command -v "$t" >/dev/null || die "required tool not found: $t"; done; }

need_pg() {
  need_tools docker timeout
  docker exec "$PG_CONTAINER" pg_isready -U "$PG_USER" -q >/dev/null 2>&1 \
    || die "Postgres container '$PG_CONTAINER' is not running (start it with: make db)"
}

# Lists backup set directories (stamp names) under <dir>, oldest first.
list_sets() {
  local d=$1 n
  [[ -d $d ]] || return 0
  for n in "$d"/*; do
    [[ -d $n && ! -L $n ]] || continue
    valid_stamp "${n##*/}" && echo "${n##*/}"
  done | sort
}
