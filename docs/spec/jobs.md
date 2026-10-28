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
| `script_duration` (M1a: spoken dialogue is not 85 to 110% of the target) | No retry of the stage. The queue fails the run with the measured reason and starts one new run with that reason as scriptwriter feedback, at most twice per chain (`MaxAutoRewrites`). The last run stays failed for a person to decide. |
| `uncertain_outcome` | **No automatic retry**; see below |

Retries use bounded exponential backoff with jitter.

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
