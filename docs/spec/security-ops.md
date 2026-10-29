# Security, Observability, and Backup

## Security by milestone
**M1a (localhost only):**
- Bind every service to 127.0.0.1.
- No authentication in M1a (owner decision 2026-10-28: the admin token was removed). Superseded in M2 by passkey authentication (D9, 2026-10-29; see "Authentication" below).
- Parameterized SQL only.
- Request validation on every endpoint; no mass assignment (explicit request structs).
- Safe error responses (no stack traces or paths to the client).
- Path safety: storage paths are built from IDs under the storage root; reject anything that resolves outside it.
- Subprocesses: fixed binary paths, argument arrays, `context` timeouts. Never run LLM-generated commands.

**M2 (before anything is reachable off-machine):**
- Real authentication and backend authorization checks on: provider settings, budgets, keys, job cancel/retry, artifact deletion, canon changes, approval, export, security settings. Hidden buttons are not security. **Done for every route that exists today (D9, #16): see "Authentication".** Provider, budget and key routes do not exist yet; they land behind the same default-deny middleware.
- HTTPS for any remote access (#17).
- CSRF protection if cookie auth is used. Partly done (#16): `SameSite=Strict` cookie plus a strict `Origin` check on every state-changing request. CSRF tokens are #17.
- Rate limits and request size limits.
- Upload validation (type, size, content sniffing) if uploads exist.
- SSRF protection on any URL the app fetches (allowlist).
- Security headers.

## Authentication (D9: passkey, decided 2026-10-29)
One owner, no user management. Passkeys (WebAuthn, library `github.com/go-webauthn/webauthn` v0.18.2, BSD-3-Clause; no hand-written crypto) with a server-side session cookie. Code: `internal/auth` (service), `internal/api/auth.go` (middleware, handlers), migration `0015_auth.sql`.

**Bootstrap.** The first passkey cannot be registered just by calling the API. `go run ./cmd/server enroll` creates a one-time code (20 characters, 104 bits, valid 10 minutes, single use) and prints it to that terminal only. The database stores its SHA-256; it never goes to the logger, the audit log or any HTTP response. Registering needs that code and is refused outright while any passkey exists, so a leaked or old code is useless after the first enrollment; creating a new code is refused too. The first enrollment signs the owner in. Further passkeys need an existing session (Settings, add passkey). The last passkey cannot be removed. Removing a passkey revokes its sessions.

**Sessions.** A random 256-bit token in the `studio_session` cookie (`HttpOnly`, `SameSite=Strict`, `Path=/`, `Secure` when the request is TLS or comes from a configured `https://` origin; plain `http://localhost` still works). The database stores only SHA-256(token). Expiry: idle 12 h (`STUDIO_SESSION_IDLE`) and absolute 7 days (`STUDIO_SESSION_MAX`). Logout revokes server-side. Unknown, expired, revoked and tampered tokens all give the same 401 with no reason.

**Ceremonies.** Passkeys must be discoverable and require user verification; login uses discoverable credentials, so begin-login reveals nothing about stored keys. A signature counter that goes backwards (possible clone) refuses the login and is flagged in `auth_credentials.clone_warning`. Ceremony state is in server memory (5 min, single use); a restart drops pending ceremonies.

**Configuration.** `STUDIO_AUTH_RP_ID` (default `localhost`) and `STUDIO_AUTH_ORIGINS` (default `localhost` and `127.0.0.1` on the server port and on 5173, the Vite dev server). A WebAuthn RP ID must be a domain, so passkeys only work from `http://localhost:PORT`; `127.0.0.1` is allowed as an origin for the CSRF check but browsers will not run a passkey ceremony there. Plain `http` origins other than loopback are rejected at startup.

**Request pipeline** (`internal/api/auth.go`), in order: (1) Origin guard: every POST/PUT/PATCH/DELETE under `/api` needs an `Origin` equal to a configured origin, and `Sec-Fetch-Site` (if sent) must be `same-origin` or `none`; otherwise 403. Non-browser clients must send an Origin header. (2) Session middleware: default deny for everything under `/api`, including unknown paths, unless the exact method and path is in the public list. A missing `Auth` service denies everything. (3) Handlers take the actor from the session (`owner:passkey-<id>`), never from the request; `audit.Record` refuses an empty actor.

**Endpoint authorization matrix.** `S` = session required (401 otherwise).

| Route | Access |
|---|---|
| `GET /api/health` | public |
| `GET /api/auth/status` | public (reports whether a passkey exists and whether this request is signed in) |
| `POST /api/auth/login/begin`, `POST /api/auth/login/finish` | public, rate limited |
| `POST /api/auth/enroll/begin`, `POST /api/auth/enroll/finish` | public only with a valid one-time code while no passkey exists, otherwise `S`; rate limited |
| `POST /api/auth/logout`, `GET /api/auth/credentials`, `DELETE /api/auth/credentials/:id` | S |
| `GET /api/system`, `/settings`, `/episodes`, `/episodes/:id`, `/runs/:id/artifacts`, `/artifacts/:id/content`, `/videos/:id/review`, `/characters`, `/audit`, `/notifications` | S (read-only; the review player works for the signed-in owner) |
| `PUT /api/settings`, `POST /api/episodes`, `POST /api/episodes/:id/rerender` | S |
| `POST /api/runs/:id/cancel`, `POST /api/runs/:id/retry` (job cancel / retry / resume) | S |
| `POST /api/videos/:id/decision` (approve / reject / changes), `POST /api/videos/:id/export` | S; approval is still bound to the artifact hash |
| `POST /api/characters`, `PUT /api/characters/:id`, `PUT /api/episodes/:id/summary`, `PUT /api/episodes/:id/in-story-date` (canon) | S |
| `PUT /api/notifications/events/:event`, `POST /api/notifications/test` (notification settings) | S |
| provider settings, budgets, keys, artifact deletion | routes do not exist yet; anything under `/api` that is not listed answers 401 |
| everything else under `/api` | 401 |
| non-`/api` paths (the SPA bundle) | public; contains no data |

**Audit events** (actor in brackets): `auth.enroll_code.create` [cli], `auth.enroll` [enroll], `auth.login.success`, `auth.logout`, `auth.passkey.add`, `auth.passkey.remove`, `auth.session.revoke` (passkey removal) [owner:passkey-N], `auth.login.failure` and `auth.enroll.failure` [anonymous, with a fixed reason word]. Never the code, the token, a credential or a signature.

**Limits.** Login/enrollment failures are counted per client IP in memory (10 per 10 minutes, then 429); on one machine that is effectively global, so an attacker on localhost can lock the owner out for up to 10 minutes. General rate limits, request-size limits, security headers, CSRF tokens and HTTPS setup are #17. Adding a passkey needs only a session (no fresh re-authentication). Sessions are not bound to an IP or device. Backups hold session token hashes and credential public keys only. Test sessions (`kind = 'test'`) can only be created by code with database access: `scripts/testsession`, which refuses any database that is not a loopback `verify_*` / `studio_restore_*` / `studio_rt_src_*` throwaway, and tests through `internal/auth/authtest`. The server has no bypass flag.

**Lost every passkey.** There is no remote recovery by design. With shell access to the database: `DELETE FROM auth_credentials;` (this also deletes every session), then run `go run ./cmd/server enroll` again.

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

**Status (M2, migration 0010):** `audit_log` exists and refuses UPDATE, DELETE and TRUNCATE through triggers (the app connects as the table owner, so revoked privileges would not hold). Code: `internal/audit`. Rows are written in the same transaction as the action for review decisions (`review.approved|rejected|changes_requested`), `export.create`, `settings.update`, canon writes (`canon.character.create|update`, `canon.summary.put`, `canon.in_story_date.put`), `job.cancel` and `job.retry` (through `Queue.RequestCancelTx` / `Queue.ResumeTx`, which run the audit insert inside the queue's own transaction), and `notification.toggle` (`Service.SetEnabledTx`; detail holds the event name and the new value). If the audit insert fails, the queue change or toggle rolls back and the request returns an error. Read-only `GET /api/audit?limit=&before=` (newest first). `actor` is a column; API handlers take it from the authenticated session (`owner:passkey-<id>`), see "Authentication". `detail` is a short object of scalars (strings capped at 200 chars, 2000 chars total by CHECK); no request bodies, secrets or absolute paths.
Limitations: the plain `Queue.RequestCancel` and `Queue.Resume` (no hook) are kept for tests and write no audit row; the API never uses them. Auth events are audited (see "Authentication"). Not yet audited: budget/provider changes and the kill switch (those features do not exist yet). `before/after` values are not stored, only the action's key facts. A superuser can still disable the triggers; backups cover that case.

## Artifact immutability
**Status (M2, migration 0011):** a trigger on `artifacts` refuses DELETE, TRUNCATE, and any UPDATE that changes a column other than `validation_status`. `validation_status` is the one explicit exception because the QA stage sets it after the video row exists (`internal/pipeline/qa.go`). Tests that simulate tampering call `storetest.AllowArtifactTampering`, which disables the trigger for that test database only.
**Status (M2, migration 0014):** `approvals`, `exports`, `qa_results` and `artifact_parents` are append-only: triggers refuse every UPDATE, DELETE and TRUNCATE, with no exempt column. The code only INSERTs into them (no UPDATE or DELETE exists, including re-render, reject, export and QA re-run, which append new rows), and no foreign key cascades into them, so nothing legitimate needed an exception. Restore from `pg_restore` only inserts, so it is unaffected.
Limitations: `validation_status` can still change on an already-approved artifact (approval is bound to the sha256, which cannot change). A database superuser can disable the trigger.

## Backup and restore (M2)
- Postgres: `pg_dump` on a schedule, with retention (e.g. 7 daily, 4 weekly).
- Artifacts: sync the storage root to a second disk or remote, with the same schedule.
- Config: back up without secrets.
- Document the link between DB rows and artifact files; a restore needs both from compatible points in time.
- **A backup counts only after a tested restore**: restore into a fresh database and storage dir, start the app, open an approved episode, and verify its hash.
- Keep backups out of the app's write path so a bug can't delete them.

Status: implemented in `scripts/backup/` (`make backup`, `make restore-test`); how it works, the DB-to-file link and the recorded test results are in `docs/backup-restore.md`. The schedule is a cron line the owner installs; the sync target is a configurable directory (`STUDIO_BACKUP_ROOT`), not a remote.
