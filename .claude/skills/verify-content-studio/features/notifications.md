# Discord notifications (Providers & budget)

The `#/providers` screen shows webhook status (configured with only the last 4 characters, or not configured), per-event toggles, a test-message button and recent deliveries.

## Driving it with drive.mjs

- Fresh instance, no webhook: `node scripts/drive.mjs $E notifications` checks "not configured", unimplemented events disabled and labelled "Not implemented", a toggle persisted and audited (`notification.toggle`), the real 409 shown by the test button, TH to EN switch.
- Configured display: launch with `STUDIO_DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/123456789012345678/fakefakefakefakefakeXYZ1 $SK/scripts/launch.sh`. The flow then checks the masked suffix and that no URL is in the page. It does NOT press the test button (that would call the real discord.com).

## Gotchas

- The webhook validator accepts only discord.com URLs, so a local fake receiver cannot be used. A real HTTP result for the test message is therefore only seen against a real webhook (not covered).
