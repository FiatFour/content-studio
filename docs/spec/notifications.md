# Notifications (Discord) — M2

The studio sends **outbound-only** notifications to Discord through channel webhooks.
Nothing on the owner's machine is exposed to the internet for this.

## Rules
1. **Notify, never act.** Notifications carry no approve, reject, retry or spend actions. Every decision happens in the studio UI, where approval is tied to the exact artifact hash. No Discord bot, slash commands or interaction endpoint.
2. **The webhook URL is a secret.**
   - It lives in env or the secret file, like an API key.
   - It never appears in logs, the frontend, prompts, artifacts or error messages.
   - The UI shows only "configured" plus the last 4 characters.
3. **Text only by default.** Never send unapproved frames, audio, video or full scripts.
   - Messages contain the episode number and title, the version, the state, and a short reason.
   - Sending thumbnails is a later opt-in setting, off by default, and only ever for approved versions.
4. **Notifications must never block the pipeline.** Delivery runs as its own job with its own retries. If Discord is down, the episode keeps going and the failure is visible in the UI.
5. **No secrets or raw errors in messages.** Use the sanitized error class and message only.

## Events

| Event | Default | Color |
|---|---|---|
| Episode ready for review (QA passed) | on | amber |
| Job failed (retries exhausted) | on | red |
| Uncertain paid-API outcome needs resolution | on | red |
| Budget threshold crossed (70 / 90 / 100%) | on | amber / red |
| Emergency stop turned on or off | on | red |
| Version approved / exported | off | green |
| Daily summary (episodes done, failures, spend) | off | blue |

Each event type can be toggled. The settings are stored in the backend and changed through the API, like every other setting.

## Delivery
- One notification per event, keyed by `event_id` for idempotency, so a retry never double-posts.
- Respect Discord rate limits: on HTTP 429 wait for the `retry_after` it returns, then retry with bounded backoff.
- Collapse floods: at most one "job failed" message per episode per 10 minutes; additional failures are counted into the next message.
- Record every delivery attempt with event, status, HTTP code and timestamp. Never record the URL.
- Links point to the studio URL from config. If it is `localhost`, say in the UI that phone links only work on the same machine or a private network such as Tailscale.

## UI (Providers & budget screen)
- Webhook status: not configured / configured (…last 4) / last delivery result.
- Event toggles.
- A **Send test message** button that posts a clearly labelled test and shows the real HTTP result.
- A recent-deliveries list.

## Tests
- No delivery when the webhook is unset; the UI says "not configured".
- The webhook URL never appears in logs or API responses (assert on captured log output).
- 429 handling and idempotent retry (no duplicate posts for the same `event_id`).
- Delivery failure doesn't fail or stall the episode job.
- Message bodies for each event contain no secrets, file paths or raw stack traces.

## Implementation status (backend, issue #15)
Backend (issue #15) and the notifications UI (issue #42, route `#/providers`) are built. The UI shows webhook status (only the masked suffix the API returns), event toggles (events with `implemented: false` are disabled and labelled "Not implemented"), a test-message button that shows the real HTTP result or the 409 "not configured" state, and the 20 most recent deliveries. The rest of the Providers & budget screen (budget, providers table, task policies) is not built and says so.

- **Webhook URL** comes from the env var `STUDIO_DISCORD_WEBHOOK_URL` (there is no secret file in the codebase yet). It is validated at startup (https, host `discord.com` or `discordapp.com`, path `/api/webhooks/<id>/<token>`, no port, userinfo or query) as SSRF protection; redirects are never followed. It is never stored in the database. The API returns only `configured` and the last 4 characters (`suffix`). Errors from the HTTP client are reduced to a class (`timeout`, `network error`, `discord returned HTTP n`) because they otherwise embed the URL.
- **API:** `GET /api/notifications` (configured, suffix, event toggles with `implemented`, last delivery, 20 recent deliveries), `PUT /api/notifications/events/:event` (`{"enabled": bool}`), `POST /api/notifications/test` (labelled test, returns the real HTTP result; 409 `not configured` when unset). There is no inbound endpoint of any kind.
- **Delivery:** an outbox table (`notifications`, unique `event_id`) is drained by a loop in `cmd/worker` that is separate from the job queue. Up to 5 attempts, backoff 30 s doubling to 5 min, and on HTTP 429 it waits at least Discord's `retry_after` (capped at 5 min). Each attempt is recorded in `notification_deliveries` (event, ok, HTTP code, error class, time; never the URL). With no webhook configured, queued rows are marked `skipped`. Delivery is at-least-once: a crash between Discord accepting and the row being marked sent can repeat one message.
- **Messages** are text only, one embed with episode number and title, version, state and a short reason; the reason keeps only its first line with URLs and file paths replaced, and mentions are disabled. No links yet: the studio URL setting does not exist.

| Event | Status |
|---|---|
| Episode ready for review (QA passed) | **Wired** in the QA stage, queued in the same transaction that marks the video `qa_passed` |
| Version approved / exported | **Wired** in the API decision and export handlers (default off) |
| Job failed (retries exhausted) | **Wired** in the queue (`Queue.Fail` and lease recovery), queued in the same transaction that sets the job `failed`, inside a savepoint so a notification problem is logged and never fails the queue. Retries, cancels and waits do not notify. Collapse: `job_failed_collapse` (migration 0013) allows one message per episode per 10 minutes; failures inside the window are counted and the count is added to the next message ("N more failure(s) since the last message were not reported"). Nothing is recorded while the toggle is off. The message holds the stage, error class and sanitized message only (paths and URLs replaced, first line). A failure that triggers an automatic rewrite still notifies, once per window |
| Uncertain paid-API outcome | Not implemented (budget/provider work) |
| Budget threshold crossed | Not implemented (budget work) |
| Emergency stop on/off | Not implemented (kill switch does not exist yet) |
| Daily summary | Not implemented (needs a scheduler) |

Toggles for unimplemented events can be stored but nothing raises those events. The dispatcher runs inside the worker, so notifications stay queued while the worker is stopped.
