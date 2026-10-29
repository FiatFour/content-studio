# Jobs, Checkpoints, and Recovery

## Model
- A **workflow run** is one episode going through its profile's stage list.
- Each stage is a **job** row. A job has one or more **attempts**.
- Stage outputs are artifacts (see `artifacts-review.md`). A stage is complete only when its output artifact is stored and validated.

## Job states

| State | Meaning |
|---|---|
| `queued` | Ready to run. |
| `running` | Leased by a worker. |
| `waiting_for_resource` | Blocked on GPU slot or provider concurrency limit. |
| `waiting_for_human` | Needs review or a decision. |
| `retry_scheduled` | Failed with a retryable error; has a `run_after` time. |
| `failed` | Terminal; retries exhausted or non-retryable error. |
| `cancelled` | Terminal; cancelled by the user. |
| `completed` | Terminal; output stored and validated. |

Legal transitions (enforce in one Go function, unit-test every pair):

```
queued               -> running | waiting_for_resource | cancelled
running              -> completed | failed | retry_scheduled | queued (lease expired) | waiting_for_resource | waiting_for_human | cancelled
waiting_for_resource -> queued | cancelled
waiting_for_human    -> queued | completed | cancelled
retry_scheduled      -> queued | cancelled
failed               -> queued   (manual retry only)
```

Record on each job:
- IDs (job, run, project), profile, stage, state.
- Timestamps: created, updated, started, finished.
- Attempt count, error class, sanitized error message.
- Output artifact IDs.
- Provider and model used.
- Estimated and actual cost (M2).
- Resource usage where available.

Progress is real stage progress (e.g. 4 of 12 images done). Never animate a fake percentage.

## M1a scope
- Postgres queue (`FOR UPDATE SKIP LOCKED` or River).
- One worker, sequential stages.
- Checkpointing: on restart, completed stages are skipped; the interrupted stage either reruns from scratch or is marked failed with a reason.
- Cancellation from the UI.

## M2 scope

### Leases and recovery
- A worker holds a lease with a heartbeat. A job whose lease expires goes back to `queued` (or `failed` if it has hit its attempt limit).
- Per-resource concurrency limits: GPU slot = 1 by default; per-provider limits are configurable.

Status (implemented, migration 0006): `Claim` stamps `lease_owner` and `lease_expires_at` (`STUDIO_LEASE_SECONDS`, default 60). The worker heartbeats every third of the TTL. Every `RunOne` first calls `Queue.RecoverExpired`, so any live worker recovers a dead worker's job: back to `queued` (the attempt is closed with class `worker_restart`), or `failed` with a reason once `STUDIO_MAX_ATTEMPTS` is used up, or `cancelled` if the run's cancel was requested. `Complete` and `Fail` are fenced on (attempt, lease owner); a worker that lost its lease gets `ErrLeaseLost` and writes nothing, and its stage context is cancelled. Consequences: after a crash the job waits up to one TTL before another worker takes it (no immediate takeover on restart, because a restarted worker cannot tell its predecessor from a live peer), and a graceful shutdown also leaves the lease to expire. A stage that ignores context cancellation after losing its lease can still write files into its attempt directory; DB state stays correct because of the fence.

Status (resource limits implemented, migration 0007): `Plan.Resources` maps a stage to the resource it holds (`script`, `tts`, `images` hold `gpu`); `jobs.resource` records it. `resource_limits.max_concurrent` is the limit (seeded `gpu` = 1; a resource with no row gets 1). `Claim` decides under a per-resource Postgres advisory lock, so two workers can never both take the last slot. A queued job whose resource is full goes `queued -> waiting_for_resource` with `wait_reason` (shown in the API as `wait_reason` and on the episode page) and the next job is tried, so CPU stages never wait behind GPU ones. Every `Claim` wakes waiting jobs back to `queued` when their resource has a free slot. A job counts against its resource only while `running`, so a crashed worker's slot frees when its lease expires. The in-process `providers.GPU` model handoff stays: it still unloads one model before loading the next inside one worker. Per-provider limits (M2 providers slice) will be more rows in the same table. Limits are changed with SQL for now; there is no settings UI. Limitation: a Claim that touches two different resources in one transaction takes two advisory locks in scan order, so two workers could deadlock; Postgres aborts one and the worker loop retries (only one resource exists today).

### Error classes
| Class | Retry? |
|---|---|
| `invalid_input` | No |
| `auth_or_permission` | No |
| `rate_limited` | Yes, honoring provider retry-after |
| `transient_network` | Yes |
| `provider_outage` | Yes, longer backoff |
| `insufficient_local_resources` | Yes, via `waiting_for_resource` |
| `validation_failed` (bad model output) | Yes, bounded (re-prompt) |
| `unsupported` | No |
| `script_duration` (M1a: spoken dialogue is not 85 to 110% of the target) | No retry of the stage. The queue fails the run with the measured reason and starts one new run with that reason as scriptwriter feedback, at most twice per chain (`MaxAutoRewrites`). The last run stays failed for a person to decide. |
| `uncertain_outcome` | **No automatic retry**; see below |

Retries use bounded exponential backoff with jitter.

Status (implemented): the table above is `remedies` in `internal/jobs/state.go`, and a test runs every class through the queue. `Queue.Fail` applies it: a retrying class under `STUDIO_MAX_ATTEMPTS` goes to `retry_scheduled` (or `waiting_for_resource` for `insufficient_local_resources`, which wakes only once `run_after` has passed and the resource has a free slot); at the bound, or for any non-retrying class, the job and run fail with the class and sanitized message. Delay (`RetryDelay` in `backoff.go`) is `base * 2^(n-1)` capped per class, with equal jitter in [d/2, d]: `transient_network` 2s..2m, `validation_failed` 1s..30s, `rate_limited` 5s..10m, `provider_outage` 30s..15m, `insufficient_local_resources` 15s..5m. `rate_limited` uses `StageError.RetryAfter` as a floor, capped at 1h because the value is untrusted. `uncertain_outcome` is `FailRun`: the queue never retries it. A person pressing Resume still can (a person decides); the M2 budget slice owns the manual-resolution flow. No provider emits `rate_limited`, `provider_outage`, `auth_or_permission` or `uncertain_outcome` yet (M1 has no cloud adapters); the classes and their handling exist so adapters can use them. `script_duration` is unchanged.

M1a stage order is script, tts, images, subtitles, render, qa. The voice stage runs before images so that a script of the wrong spoken length is caught before the expensive image work. Character count cannot predict duration well enough (11.6 and 15.5 characters per second in two real runs), so length is judged from the measured audio.

### Idempotency
- Every stage execution has an idempotency key (run ID + stage + input hash).
- Re-running a completed key returns the existing artifact instead of regenerating.

### Uncertain outcomes (paid APIs)
If a billable request times out after it may have been accepted:
- Do not blindly resend it.
- Use the provider's idempotency key or a status lookup if it has one.
- Otherwise mark the attempt `uncertain_outcome`, keep its budget reservation, and surface it for manual resolution.

### Failure tests (M2)
Simulate each and assert no corrupted state:
- Local model unavailable.
- Cloud provider disabled.
- Budget exhausted.
- Provider timeout and uncertain outcome.
- Invalid AI output.
- Worker crash and process restart mid-job.
- Disk full.
- Missing media asset.
- Corrupt output file.
- Concurrent jobs competing for GPU and budget.
- Unauthorized approval attempt.

### Run chain (M1a)
An episode has many runs. Each run has exactly one origin, stored in `workflow_runs.kind` and `parent_run_id`, and the database enforces the pairing:

| kind | parent_run_id | feedback | stages | meaning |
|---|---|---|---|---|
| `full` | none | none | script, tts, images, subtitles, render, qa | a fresh production |
| `rerender` | the run whose inputs it reuses | none | render, qa | a new video version from the same script, images and audio |
| `rewrite` | the failed run it replaces | required | the full list | a new script after the voice came out the wrong length, with measured feedback for the scriptwriter |

The number of rewrites in a row is derived by walking the parents (`rewriteDepth`), never stored. The queue starts a rewrite by itself at most `MaxAutoRewrites` (2) times in a row. After that the run stays failed for a person.

`Queue.Resume(run)` is the manual retry and picks the remedy from the failed job's error class (`ErrorClass.Remedy()`): a retryable stage goes back in the queue and keeps the run; `script_duration` starts a rewrite run instead, because running the voice stage again on the same script would fail the same way. A run can be rewritten once.

Design choices: one typed origin instead of three loose columns (`source_run_id`, `rewrite_of`, `auto_rewrites`) that allowed contradictory combinations; no separate links table, because the relationship is always one parent per run. The UI lists every run of an episode with its kind, parent, feedback and failure reason.

Why the voice stage checks length at all: in four real runs qwen3:8b ignored character totals and wrote 8 shots with one line each. The script prompt therefore asks for structure (two lines per shot), `ValidateScript` rejects fewer than 1.7 lines per shot before any voice work, and the measured-duration check (`dialogueFit`) is the final gate.
