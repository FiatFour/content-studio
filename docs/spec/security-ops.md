# Security, Observability, and Backup

## Security by milestone
**M1a (localhost only):**
- Bind every service to 127.0.0.1.
- No authentication in M1a (owner decision 2026-10-28: the admin token was removed). Any local process can call approve and export, so the localhost binding is the only protection. A web page open in the owner's browser could also reach the API; add auth before relying on approval for anything beyond a solo local machine.
- Parameterized SQL only.
- Request validation on every endpoint; no mass assignment (explicit request structs).
- Safe error responses (no stack traces or paths to the client).
- Path safety: storage paths are built from IDs under the storage root; reject anything that resolves outside it.
- Subprocesses: fixed binary paths, argument arrays, `context` timeouts. Never run LLM-generated commands.

**M2 (before anything is reachable off-machine):**
- Real authentication and backend authorization checks on: provider settings, budgets, keys, job cancel/retry, artifact deletion, canon changes, approval, export, security settings. Hidden buttons are not security.
- HTTPS for any remote access.
- CSRF protection if cookie auth is used.
- Rate limits and request size limits.
- Upload validation (type, size, content sniffing) if uploads exist.
- SSRF protection on any URL the app fetches (allowlist).
- Security headers.

## Untrusted input
Treat as data, never instructions: scripts, model output, downloaded pages, documents, image metadata, captions, external API responses.
Untrusted content can never change provider enablement, budgets, auth, permissions, approval rules, config, secret access, or tool permissions. Enforce this in code, not prompts.

## Observability
Structured logs (`slog`, JSON) for:
- Job creation and state transitions.
- Agent task execution (provider, model, duration, error class).
- Provider requests (without bodies containing secrets).
- Retries, timeouts, uncertain outcomes.
- Budget reservations and reconciliation.
- Artifact creation.
- QA results.
- Canon changes.
- Approvals and rejections.
- Config changes and kill-switch activation.
- Auth failures.

Redact secrets everywhere. Don't log full prompts by default; store them as artifacts instead.

The operations dashboard (M3) should answer:
- Why did this job fail, and at which stage?
- Which provider and model were used?
- Was cloud fallback used, and why?
- What did it cost?
- Which artifact version came out, and is it approved?
- Can it safely resume?

## Audit log
Append-only table for approvals, rejections, exports, canon commits, budget and provider changes, kill switch, and auth failures. Who, what, when, before/after values.

**Status (M2, migration 0010):** `audit_log` exists and refuses UPDATE, DELETE and TRUNCATE through triggers (the app connects as the table owner, so revoked privileges would not hold). Code: `internal/audit`. Rows are written in the same transaction as the action for review decisions (`review.approved|rejected|changes_requested`), `export.create`, `settings.update`, and canon writes (`canon.character.create|update`, `canon.summary.put`, `canon.in_story_date.put`). Read-only `GET /api/audit?limit=&before=` (newest first). `actor` is a column (`owner` until D9). `detail` is a short object of scalars (strings capped at 200 chars, 2000 chars total by CHECK); no request bodies, secrets or absolute paths.
Limitations: `job.cancel` and `job.retry` are written in a separate transaction right after the queue commits, because `Queue.RequestCancel` and `Queue.Resume` own their transactions; a crash between the two loses that row. Fix when the queue rework lands (take a hook or a tx). Not yet audited: budget/provider changes, kill switch, auth failures (those features do not exist yet). `before/after` values are not stored, only the action's key facts. A superuser can still disable the triggers; backups cover that case.

## Artifact immutability
**Status (M2, migration 0011):** a trigger on `artifacts` refuses DELETE, TRUNCATE, and any UPDATE that changes a column other than `validation_status`. `validation_status` is the one explicit exception because the QA stage sets it after the video row exists (`internal/pipeline/qa.go`). Tests that simulate tampering call `storetest.AllowArtifactTampering`, which disables the trigger for that test database only.
Limitations: `artifact_parents`, `qa_results`, `approvals` and `exports` are not yet guarded by triggers. `validation_status` can still change on an already-approved artifact (approval is bound to the sha256, which cannot change). A database superuser can disable the trigger.

## Backup and restore (M2)
- Postgres: `pg_dump` on a schedule, with retention (e.g. 7 daily, 4 weekly).
- Artifacts: sync the storage root to a second disk or remote, with the same schedule.
- Config: back up without secrets.
- Document the link between DB rows and artifact files; a restore needs both from compatible points in time.
- **A backup counts only after a tested restore**: restore into a fresh database and storage dir, start the app, open an approved episode, and verify its hash.
- Keep backups out of the app's write path so a bug can't delete them.

Status: implemented in `scripts/backup/` (`make backup`, `make restore-test`); how it works, the DB-to-file link and the recorded test results are in `docs/backup-restore.md`. The schedule is a cron line the owner installs; the sync target is a configurable directory (`STUDIO_BACKUP_ROOT`), not a remote.
