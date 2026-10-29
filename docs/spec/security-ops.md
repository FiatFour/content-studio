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

## Backup and restore (M2)
- Postgres: `pg_dump` on a schedule, with retention (e.g. 7 daily, 4 weekly).
- Artifacts: sync the storage root to a second disk or remote, with the same schedule.
- Config: back up without secrets.
- Document the link between DB rows and artifact files; a restore needs both from compatible points in time.
- **A backup counts only after a tested restore**: restore into a fresh database and storage dir, start the app, open an approved episode, and verify its hash.
- Keep backups out of the app's write path so a bug can't delete them.

Status: implemented in `scripts/backup/` (`make backup`, `make restore-test`); how it works, the DB-to-file link and the recorded test results are in `docs/backup-restore.md`. The schedule is a cron line the owner installs; the sync target is a configurable directory (`STUDIO_BACKUP_ROOT`), not a remote.
