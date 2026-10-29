# Backup and restore

Scripts: `scripts/backup/backup.sh`, `restore.sh`, `restore-test.sh` (helpers in `lib.sh`, fixture seeder in `seedfixture/`).
Make targets: `make backup`, `make restore-test`, `make restore-test-fixture`. None of them is part of `make test`.
Spec: `docs/spec/security-ops.md` (Backup and restore).

A backup counts only after a tested restore. Run `make restore-test` after the first backup and after any change to the schema, the storage layout or these scripts.

## What a backup set contains

`STUDIO_BACKUP_ROOT` (default `~/studio-backups`) holds:

```
daily/<UTC stamp>/     last 7 sets
weekly/<UTC stamp>/    last 4 sets (the first daily set of each ISO week, hard-linked)
  db.dump              pg_dump --format=custom of the studio database
  storage/             copy of the artifact storage root
  config/studio.env    non-secret settings
  config/skipped.txt   names (never values) of settings that were left out
  MANIFEST.txt         time, database, dump sha256, row and file counts, applied migrations
```

- Sets are built under `daily/.incomplete-<stamp>` and renamed when complete, so a partial set never looks valid.
- Storage files are immutable and never overwritten, so each set hard-links unchanged files to the previous set with `rsync --link-dest`. Every set is complete on its own; only new files use disk space. Deleting an old set never damages a newer one.
- Retention (7 daily, 4 weekly) is done by `backup.sh` itself and removes only directories named like a UTC stamp.
- The backup root is refused if it is inside the storage root, if the storage root is inside it, or if it is inside the repository. Put it on another disk for real protection: it only protects against app bugs and accidental deletion while it shares a disk with the data.
- Not backed up: `var/exports` (copies of approved videos that can be re-exported from storage), model weights, ComfyUI and Ollama data, and the code (git).

Environment: `STUDIO_DATABASE_URL` (only the database name is read from it), `STUDIO_STORAGE_ROOT`, `STUDIO_BACKUP_ROOT`, optional `STUDIO_ENV_FILE`, `STUDIO_PG_CONTAINER` (default `studio-pg`), `STUDIO_PG_USER` (default `studio`). Postgres runs in Docker, so `pg_dump`, `pg_restore` and `psql` are run with `docker exec`; the host needs no Postgres client.

### Schedule

`backup.sh` does not install a scheduler. Add one yourself, for example `crontab -e`:

```
15 3 * * * cd /home/fiat/project/content-studio && ./scripts/backup/backup.sh >>~/studio-backups/backup.log 2>&1
```

The scripts log no environment values, so the log is safe to keep. Check `backup.log` occasionally; a failed backup exits non-zero and writes nothing complete.

## How the config backup avoids secrets

The app is configured only through environment variables (`internal/config`). For each candidate setting:

1. Only names on the allowlist in `backup.sh` (the keys `internal/config` reads) are taken, from the process environment and from an optional `STUDIO_ENV_FILE` (parsed line by line, never sourced).
2. A name containing PASSWORD, SECRET, TOKEN, KEY, WEBHOOK, CREDENTIAL or AUTH is skipped.
3. A value containing credentials in a URL (`scheme://user:pass@host`) is skipped.
4. `STUDIO_DATABASE_URL` is recorded as host and database only (`postgres://<credentials omitted>@host/db`).

Skipped names go to `config/skipped.txt`, values never. The script never prints the environment. `restore-test.sh` also fails if any file in the set's `config/` contains a URL with credentials. When a new setting is added to `internal/config`, add its name to the allowlist; a secret added later (for example the Discord webhook URL in M2) is ignored by default because it is not on the list. Actual secrets must be re-created by hand after a restore.

## DB rows and files

`artifacts.storage_path` is a path relative to the storage root, `episodes/<episode>/runs/<run>/jobs/<job>/a<attempt>/<type>[-<key>].<ext>`. `artifacts.sha256` is the hash of that file. `approvals.sha256` is the hash the owner approved. The app serves a file only from `<storage root>/<storage_path>`.

A working restore needs the database and the storage from compatible points in time:

- A file is written before its row, so `backup.sh` dumps the database first and syncs storage second. Every file a dumped row points at is then in the set. Files newer than the dump are harmless extras.
- Never combine a database dump from one set with the storage of another. A newer database with older storage leaves rows pointing at missing files; the API answers 404 "artifact file is missing" for them.
- Storage is append-only, so storage from the same or a later point than the database is always safe.

## Restoring

`restore.sh <backup-set-dir> <new-db-name> <new-storage-dir>` restores into a new database and a new directory and never overwrites anything. It refuses the live database name and the live storage root (from `STUDIO_DATABASE_URL`, `STUDIO_STORAGE_ROOT` or their defaults), a database that already exists, a storage directory that is not empty, a name that does not match `studio_restore_[a-z0-9_]+`, and a dump whose sha256 differs from the manifest.

To replace the live system after a disaster (manual on purpose, because it is destructive):

1. Stop `make dev` (server and worker).
2. Run `restore.sh <set> studio_restore_<date> <new storage dir>`.
3. Point the app at the restored copy: `STUDIO_DATABASE_URL=.../studio_restore_<date>` and `STUDIO_STORAGE_ROOT=<new storage dir>`. Or, once you are sure, rename the old database aside (`ALTER DATABASE studio RENAME TO studio_old` and `ALTER DATABASE studio_restore_<date> RENAME TO studio`) and move the storage directory into place.
4. Start the app and open an approved episode.
5. Re-create secrets by hand (they are not in the backup).

## The restore test

`restore-test.sh` (`make restore-test`) takes the newest daily set (or `--set <dir>`), then:

1. Restores it into a fresh database `studio_restore_<time>_<random>` and a fresh temp directory, using `restore.sh` and its guards.
2. Builds and starts `cmd/server` against them on the first free port in 18080-18180 (never the live address).
2a. The API needs a session (D9). The script mints a throwaway one with `scripts/testsession` directly into the restored database (the helper refuses any database that is not a loopback `studio_restore_*`, `studio_rt_src_*` or `verify_*` throwaway, so the live database cannot be used) and passes it to curl through a mode-600 header file. The server itself has no bypass.
3. Finds the newest video whose latest decision is `approved`.
4. Downloads it through `GET /api/artifacts/:id/content`, and checks that its sha256 equals the approved hash (and the artifact row's hash), that `/api/videos/:id/review` answers, and that `ffprobe` reads it.
5. Drops only the databases it created and deletes its temp directory, even on failure.

`--fixture` (`make restore-test-fixture`) needs no existing backup: it creates a throwaway database `studio_rt_src_*`, writes a 1 s test video, and records an approved episode through the app's own store, artifact and review code (`seedfixture`), runs `backup.sh` on it and then the same restore test. It never touches the live database, any other database, or the live storage.

Requirements: Docker with the `studio-pg` container (`make db`), `rsync`, `curl`, Go, and ffmpeg/ffprobe for `--fixture`.

## Test results

Recorded 2026-10-29 on this machine (Postgres 17 in `studio-pg`, no GPU):

| Run | Result |
|---|---|
| `restore-test.sh --fixture` | PASSED. Approved fixture video (sha256 `2bba23b5...`) restored and its hash verified through the running server; ffprobe duration 1.0 s. |
| Backup of the real dev database (151 artifact rows) and real storage (220 files, 204 MB) into a temp backup root, then `restore-test.sh` | PASSED. Approved episode 4, video artifact 77, sha256 `3c1d45de...` equal after restore; ffprobe duration 60 s. |
| Backup with pre-seeded old sets | 9 daily sets pruned to 7, 6 weekly sets to 4; a second backup hard-links unchanged storage files. |
| Refusals | live database name, storage inside the live root, non-empty target, odd db name, backup root inside storage or the repo, tampered `db.dump`, a set whose storage lacks the approved file (404 on download, test fails). |

Not verified: restoring over the real live database (deliberately manual), a backup root on a second physical disk or remote, a cron run, and Postgres versions other than 17.

Limitations: the schedule is a cron line you install; retention counts sets, not age (a skipped week shortens the window); the dump is not encrypted, so keep the backup root as private as the data itself.
