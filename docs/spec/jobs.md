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
queued               -> running | cancelled
running              -> completed | failed | retry_scheduled | waiting_for_resource | waiting_for_human | cancelled
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
| `uncertain_outcome` | **No automatic retry**; see below |

Retries use bounded exponential backoff with jitter.

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
