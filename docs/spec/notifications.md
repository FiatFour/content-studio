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
