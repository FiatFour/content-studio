#!/usr/bin/env bash
# Restores one backup set into a NEW database and a NEW storage directory. It never overwrites
# anything: the database must not exist yet and the storage directory must be absent or empty.
# It refuses the live database name and the live storage root (STUDIO_DATABASE_URL and
# STUDIO_STORAGE_ROOT, or their defaults).
#
# Usage: restore.sh <backup-set-dir> <new-db-name> <new-storage-dir>
#   <new-db-name> must look like studio_restore_<letters, digits, underscores>.
# To bring a restored system live, see docs/backup-restore.md (this script deliberately does not do it).
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

(( $# == 3 )) || die "usage: restore.sh <backup-set-dir> <new-db-name> <new-storage-dir>"
set_dir=$(abspath "$1")
target_db=$2
target_storage=$(resolve_path "$3")

live_db=$(db_name_from_url "${STUDIO_DATABASE_URL:-$DEFAULT_DB_URL}")
live_storage=$(resolve_path "${STUDIO_STORAGE_ROOT:-var/storage}")

[[ $target_db =~ ^studio_restore_[a-z0-9_]{1,40}$ ]] || die "target database must match studio_restore_[a-z0-9_]+"
valid_db_name "$target_db" || die "target database name is not allowed"
[[ $target_db != "$live_db" ]] || die "refusing: target database is the live database"
if is_within "$target_storage" "$live_storage" || is_within "$live_storage" "$target_storage"; then
  die "refusing: target storage overlaps the live storage root"
fi
is_within "$target_storage" "$set_dir" && die "refusing: target storage is inside the backup set"
[[ -f $set_dir/db.dump && -f $set_dir/MANIFEST.txt && -d $set_dir/storage ]] || die "not a complete backup set: $set_dir"
if [[ -e $target_storage ]]; then
  [[ -d $target_storage && -z $(ls -A -- "$target_storage") ]] || die "target storage exists and is not empty"
fi
need_tools rsync sha256sum
need_pg

# The dump must be the one the manifest recorded, so a damaged or swapped file is caught before restoring.
want=$(sed -n 's/^db_dump_sha256=//p' "$set_dir/MANIFEST.txt")
[[ $want =~ ^[0-9a-f]{64}$ ]] || die "manifest has no valid db_dump_sha256"
got=$(sha256sum "$set_dir/db.dump" | cut -d' ' -f1)
[[ $got == "$want" ]] || die "db.dump does not match the manifest sha256 (damaged backup)"

exists=$(psql_q postgres "SELECT count(*) FROM pg_database WHERE datname = '$target_db'")
[[ $exists == 0 ]] || die "database $target_db already exists; choose a new name"

echo "restoring database into $target_db"
pg createdb -U "$PG_USER" "$target_db"
if ! pg pg_restore -U "$PG_USER" -d "$target_db" --no-owner --exit-on-error <"$set_dir/db.dump"; then
  # Only a database this script just created is dropped.
  pg dropdb -U "$PG_USER" --if-exists "$target_db" || true
  die "pg_restore failed; $target_db was dropped"
fi

echo "restoring storage into $target_storage"
mkdir -p -- "$target_storage"
timeout 3600 rsync -a -- "$set_dir/storage/" "$target_storage/"

echo "restored: database=$target_db storage=$target_storage"
