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
- HTTPS for any remote access (#17). **Done as configuration and documentation, not as a terminator:** optional TLS from cert/key files, refusal of a non-loopback bind without TLS, and `docs/remote-access.md`. See "Transport security".
- CSRF protection if cookie auth is used. **Done (#16, #17): no token needed; see "Transport security", CSRF decision.**
- Rate limits and request size limits. **Done (#17), rate limits in memory.**
- Upload validation (type, size, content sniffing) if uploads exist. **No upload endpoint exists, so there is nothing to validate.** Every body is JSON; anything else is refused with 415.
- SSRF protection on any URL the app fetches (allowlist). **Done: see "Transport security", SSRF.**
- Security headers. **Done (#17).**

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

**Limits.** Login/enrollment failures are counted per client IP in memory (10 per 10 minutes, then 429); on one machine that is effectively global, so an attacker on localhost can lock the owner out for up to 10 minutes. The general rate limit, request-size limits, security headers and HTTPS setup (#17) are under "Transport security"; the login/enroll counter above is separate and stricter (either one can answer 429). Adding a passkey needs only a session (no fresh re-authentication). Sessions are not bound to an IP or device. Backups hold session token hashes and credential public keys only. Test sessions (`kind = 'test'`) can only be created by code with database access: `scripts/testsession`, which refuses any database that is not a loopback `verify_*` / `studio_restore_*` / `studio_rt_src_*` throwaway, and tests through `internal/auth/authtest`. The server has no bypass flag.

**Lost every passkey.** There is no remote recovery by design. With shell access to the database: `DELETE FROM auth_credentials;` (this also deletes every session), then run `go run ./cmd/server enroll` again.

## Transport security (#17, M2)
Code: `internal/api/secure.go` (middleware), `internal/config/config.go` (settings), `cmd/server/main.go` (server, TLS). Tests: `internal/api/secure_test.go`, `stream_test.go`, `internal/config/transport_test.go`, `internal/providers/localclient_test.go`. Default behavior is unchanged: loopback, plain `http://localhost:8080`.

**Security headers**, on every response including 401/403/404/413/415/429 and the SPA files (set before routing):

| Header | Value |
|---|---|
| `Content-Security-Policy` | `default-src 'none'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; style-src-attr 'unsafe-inline'; font-src https://fonts.gstatic.com; img-src 'self' data:; media-src 'self'; connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'` |
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY` (CSP `frame-ancestors 'none'` is the modern form) |
| `Referrer-Policy` | `no-referrer` |
| `Permissions-Policy` | `publickey-credentials-get=(self)`, `publickey-credentials-create=(self)`, `fullscreen=(self)`; camera, microphone, geolocation, payment, usb, serial, midi, display-capture and motion sensors are off |
| `Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy` | `same-origin` (COEP is not set; nothing needs cross-origin isolation) |
| `Strict-Transport-Security` | `max-age=31536000`, only when the request is TLS, or its `Host` is the host of a configured `https://` origin (TLS proxy in front). Never on plain `http://localhost`. No `includeSubDomains`, no preload. |
| `Cache-Control: no-store` | on `/api` responses; artifact content keeps its own `private, immutable` cache header |

CSP notes. The Vite build needs only same-origin script and CSS. React sets `style=` attributes, hence `style-src-attr 'unsafe-inline'` (style attributes only, not `<style>` elements or scripts). `web/index.html` loads Google Fonts, so those two hosts are allowed; the browser therefore contacts Google when the page loads (self-hosting the fonts would remove that and both allowances; not done). The review player and thumbnails use same-origin URLs, so `blob:` is deliberately not allowed. The Vite dev server (port 5173) does not send these headers, only the Go server does, so check the CSP against `make dev` or a built `web/dist`, not `npm run dev`. If a feature needs a new origin or `blob:`, widen the policy in `secure.go`; the verify skill's console check fails until you do.

**Body size.** Every state-changing `/api` request is size-checked before any handler: declared `Content-Length` first (413), then the actual bytes (chunked bodies too). `STUDIO_MAX_BODY_BYTES` (default 1 MiB, range 1 KiB to 64 MiB) is the global cap. Overrides, never above the global cap: `/api/auth/*` 64 KiB; `PUT /api/settings`, `/api/notifications/*`, `/api/runs/*`, `/api/videos/*` 16 KiB. **No upload endpoint exists** (no multipart handling anywhere in the Go code) and every body is JSON.

**Content type.** A state-changing `/api` request that carries a body must be `application/json` (a charset parameter is fine), else 415. Body-less POSTs (cancel, retry, rerender, logout) need no content type. This stops form posts and `text/plain` bodies, the types a cross-site page can send without a CORS preflight.

**Timeouts** (`http.Server`): read-header 10 s, read `STUDIO_READ_TIMEOUT` 30 s, write `STUDIO_WRITE_TIMEOUT` 60 s, idle `STUDIO_IDLE_TIMEOUT` 2 min, request headers at most 32 KiB. Artifact downloads (`GET /api/artifacts/:id/content`, the video player with range requests) replace the write deadline with `STUDIO_STREAM_TIMEOUT` (default 15 min) per request, so a slow client is not cut off at 60 s. Test: a 64 MiB video read by a client that waits longer than the write timeout completes, and the same download with a 1 ms stream deadline does not.

**Rate limits** (token buckets, in memory, reset when the server restarts). Scope: every `/api` request except `GET /api/health`; the SPA files are not limited (they hold no data). Requests with a session cookie are counted per cookie value (hashed), allowance `STUDIO_RATE_AUTH_PER_MIN`, default 1200/min (burst 300); all others per client IP, `STUDIO_RATE_ANON_PER_MIN`, default 120/min (burst 30). Over the limit: 429 with `Retry-After` in seconds. Separate buckets mean anonymous or forged-cookie traffic from the same machine cannot use up the signed-in owner's allowance. The limiter does not validate the cookie: a forged cookie just gets its own small bucket (at most 10000 buckets; beyond that new keys share one overflow bucket). The client IP is the TCP peer address; `X-Forwarded-For` is deliberately not trusted, so behind a reverse proxy or `tailscale serve` all anonymous traffic shares the proxy's address and one anonymous bucket. Combination with the login limiter (`internal/auth`, 10 failed ceremonies per IP per 10 minutes): the general limiter runs first and counts every request; the login limiter counts only failed login/enroll attempts. Whichever trips first answers 429, so on those routes the stricter one wins.

**CSRF decision: no token.** A token would add a second secret and frontend plumbing without closing a gap, because all of these hold. (1) The session cookie is `SameSite=Strict`, so browsers do not send it on cross-site requests. (2) Every state-changing `/api` request must carry an `Origin` equal to a configured origin (non-browser clients must send one too), which also covers the same-site, different-origin case (a sibling subdomain or another localhost port) that `SameSite` alone does not. (3) `Sec-Fetch-Site`, when sent, must be `same-origin` or `none`. (4) Bodies must be `application/json`, so a cross-origin page cannot make a "simple" request, and the API sends no CORS headers, so a preflight fails. (5) No state change is reachable by GET. (6) There are no bearer tokens or other cookie-less auth. What remains: a browser that omits `Origin` on a POST (none that support passkeys do), and XSS on our own origin, which a token would not stop either and which the CSP is there to prevent. If bearer-token access or CORS is ever added, revisit this.

**TLS and remote access.** `STUDIO_TLS_CERT` and `STUDIO_TLS_KEY` (both or neither; the files must be readable; paths are not echoed in errors) make the server serve HTTPS (TLS 1.2+). The certificate is read at startup; renewing needs a restart. A non-loopback `STUDIO_ADDR` (including `0.0.0.0`, an empty host, or a hostname) is refused at startup unless TLS is configured or `STUDIO_ALLOW_INSECURE_REMOTE=true` is set. The override logs a warning and leaves passkeys and the session cookie on plain http across the network, so it is for a trusted wired LAN at most. There is no built-in certificate issuance or terminator. Safe options, RP ID and origin settings: `docs/remote-access.md`.

**SSRF.** The app makes outbound requests from exactly two places: the worker to the three AI service URLs, and the notification service to the Discord webhook. (1) `OLLAMA_URL`, `COMFYUI_URL`, `THAIVOICE_URL` must be `http(s)`, carry no credentials, and have a host of `localhost` or a loopback IP, checked at startup by `config.requireLocal` (tests cover userinfo tricks, `localhost.evil`, `0.0.0.0`, `ftp:`, `file:`, IPv4-mapped addresses). The host is not resolved, so a `localhost` that a hosts file points elsewhere is not detected. (2) The worker's HTTP client never follows redirects (`providers.NewLocalClient`, tested), so a local service cannot bounce a request elsewhere. Request paths are fixed in code, ComfyUI file names are escaped query values, and nothing in a model response is used as a URL. (3) The Discord webhook is https only, on `discord.com` or `discordapp.com`, with the webhook path, no port or credentials, and no redirects (`notify.ValidateWebhookURL`, existing tests). Browsers never ask the server to fetch a URL. No new fetchers were added.

**Not done / limits.** Rate limits reset on restart and are per process; there are no per-endpoint rate limits beyond the login counter. No automatic certificate reload. No self-hosted fonts. The CSP has no `report-uri`; violations show only in the browser console. HSTS and `Secure` cookies depend on the configured origins, so a wrong `STUDIO_AUTH_ORIGINS` behind a proxy leaves them off.

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
