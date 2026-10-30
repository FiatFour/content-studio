# Providers, Policies, and Budget (M2)

M1 has local adapters only and no cloud code paths. The budget and cloud parts of this file are not built.

## Implementation status (issue #18, local-only part)
Implemented in `internal/providers/registry.go`:
- A provider registry. Each registration declares name, kind (`local` or `cloud`), and capability (`llm`, `image`, `tts`, `segment`). The kind is held by the registry, so a provider cannot claim to be local. Registration is idempotent; reusing a name for a different definition is an error. Ollama, ComfyUI and thaivoice register as local in `cmd/worker`.
- Task policies `LOCAL_ONLY` (the default for every task), `LOCAL_FIRST_CLOUD_FALLBACK`, `CLOUD_PREFERRED` and `DISABLED`. Names must match exactly; any other value, including other case or whitespace, is refused.
- One choke point, `providers.Resolve(registry, task, policy)`. Every pipeline stage (script, tts, images, subtitles) gets its provider through it, under `LOCAL_ONLY`. Nothing reads a policy from script or model output.
- A `cloud enabled` setting (`STUDIO_CLOUD_ENABLED`, default off, backend env only). A cloud-allowing policy is refused when it is off. When it is on, the request is still refused with "budget check not implemented", so the path fails closed.
- Tests: a counting cloud fake (`fake.CloudSpy`) is never called across every stage or resolution path (`internal/providers/registry_test.go`, `internal/pipeline/provider_policy_test.go`).

Not implemented: budget and reservations, the kill switch, any real cloud adapter or pricing, provider keys, health and usage metrics, per-task policy configuration (stages always use `LOCAL_ONLY`; `GET /api/policies` and the screen list them read-only and show "Not implemented" for changing them), and a "local failed, try cloud" fallback at run time. Cloud work stays blocked on D10 (issues #19-#21).

## Providers
A provider is registered with:
- Name, capability (llm, image, tts, stt, video), and local/cloud class.
- Enabled flag (cloud providers default to **disabled**).
- Model list and configuration.
- Health status and last error.
- Usage metrics: request count, latency, estimated and actual cost.

The UI must show these as **separate** things, never one ambiguous toggle:
1. Is the provider enabled?
2. Is cloud fallback allowed?
3. Which policy applies to this task?
4. Is the provider healthy?
5. Does the budget allow another request?

Unimplemented integrations show "Not implemented". Never fake a successful connection.

## Task policies

| Policy | Behavior |
|---|---|
| `LOCAL_ONLY` | Never calls a cloud provider. Enforced in code and covered by a test. |
| `LOCAL_FIRST_CLOUD_FALLBACK` | Cloud only if local fails or can't meet a configured requirement, **and** the cloud provider is enabled, **and** budget allows. |
| `CLOUD_PREFERRED` | Requires explicit configuration. Still subject to provider switch, budget, and security policy. |
| `DISABLED` | Task does not run. |

All routing goes through one Go function that takes (task, policy, provider states, budget) and returns a decision plus a reason. The reason is logged and shown in the UI.
A disabled cloud provider is unreachable from every code path, regardless of frontend state or model output.

## Budget
- Default monthly app cloud budget: **THB 300** (configurable). Report dev subscriptions, runtime API spend, hosting, storage, and electricity as separate cost lines.
- Prices, model rates, and exchange rates live in config with a source URL and effective date. Never hardcode them.
- Limits: global monthly, per provider, optional per project and per job.
- Thresholds: 70% warning, 90% critical, 100% blocks new paid requests. Local work continues.
- An **emergency kill switch** blocks all outbound paid requests immediately.
- Budget period has an explicit reset time and timezone (Asia/Bangkok by default). Restarting the app never resets anything.

## Reservation flow
Before any billable request:
1. Check provider enabled and policy allows it.
2. Estimate the cost (use a conservative configured maximum if it can't be estimated, or require manual approval).
3. In one transaction, lock the budget row, check remaining ≥ estimate, and insert a reservation.
4. Send the request.
5. Record actual usage and cost.
6. Reconcile: replace the reservation with the actual cost and release the difference.

Rules:
- Concurrent jobs can never spend the same remaining budget. Test this with parallel goroutines against a real Postgres.
- Retries and fallbacks each need their own reservation; neither can bypass the check.
- Uncertain outcomes keep their reservation until resolved. A timeout is never treated as free.
- The server never trusts client-supplied costs.
- Every admin change to limits or the kill switch goes to the audit log.

## Remaining exposure (document it)
The app's cap controls the app's own decisions. It can't guarantee the provider bills nothing beyond it: billing delays, taxes, currency conversion, and uncertain requests can all cause drift. Set provider-side spend limits too, where the provider offers them.

## Secrets
Provider keys live in env vars or a secret file with restricted permissions, separate from ordinary settings.
- Never sent to the browser, logged, put in prompts, or written to artifacts.
- The UI shows only the last 4 characters after entry.
- Rotation means replacing the value; no restart-sensitive caching beyond process lifetime.
