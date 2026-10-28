# M1a closing report

Date: 2026-10-28. Branch `m1a-run-chain`, last code commit `11b5dbd`. Scope and acceptance: `docs/spec/milestones.md`.

## Result

One real, playable episode is produced locally, approved by hash, and exported. Every acceptance bullet has evidence; the gaps below are accepted limitations, not hidden failures.

## Acceptance evidence

| Acceptance | Evidence | Kind |
|---|---|---|
| `make dev` starts everything | Run three times on 2026-10-27/28; server, worker, ComfyUI, voice service and Postgres came up and `/api/health` was ok. No automated test. | manual |
| An episode created through the API produces an MP4 that passes QA | Real models: run 10 (episode 5, artifact 116) and run 12 (episode 6, artifact 150). ffprobe: h264 + aac, 1080×1920, 30 fps, 60.000 s, `qa_passed`. The UI form path is covered by `verify-content-studio` (`create-episode`, queue only). | real run |
| Approve then export gives a file whose hash equals the approved hash | Artifact 116 approved and exported; sha256 `6729db2f…7ee026` is identical in the approval row, the `exports` row, the exported file and the original. Also `review_test.go` and `review_http_test.go`. | real run + tests |
| A re-render makes a new, unapproved version | Run 11 re-rendered episode 5: artifact 119, version 2, `qa_passed`, no approval, while artifact 116 kept its approval. Version 2 has the same sha256 as version 1 because the render is deterministic; it still needs its own approval. | real run |
| Killing the worker mid-job resumes or fails with a reason | `kill -9` of the worker during `images` (2 of 7 done) on run 12. The restarted worker logged "recovered interrupted jobs" and ran `images` again as attempt 2; `script` and `tts` were not redone. All 7 image rows point at attempt 2 and the run reached `ready_for_review`. Earlier, runs 8 and 10 were resumed twice after the whole stack stopped. Also `TestCrashedWorkerResumesFromLastCompletedStage`. | real run + tests |
| `go test ./...` passes with fake providers | `gofmt` clean, `go vet` clean, all packages ok on a real Postgres (`-count=1`). Web: `npm test`, 5 tests, TH/EN key parity. | tests |

## What was added after the first slice

- Voice-length rewrite loop: stage order is script, tts, images, subtitles, render, qa; a `script_duration` failure starts a rewrite run with the measured reason, at most twice per chain. Seen working with real models (run 8 failed on 39 s of speech for a 60 s target, run 9 failed, run 10 passed).
- Typed run chain (`full`, `rerender`, `rewrite`) enforced by CHECK constraints.
- The admin token was removed on the owner's decision (2026-10-28).
- Negative QA tests, HTTP tests for decision, export and content, and path scrubbing of job error text on output.
- README and the `verify-content-studio` skill updated for the new state.

## What does not work yet

- Characters are not visually consistent between shots (M1b).
- No authentication. Localhost binding is the only protection; any local process or a web page open in the browser can call approve and export.
- No cloud providers, budget, canon, notifications or agent tower (later milestones; the UI shows "Not implemented").
- No `projects` endpoint. `createEpisode` creates or reuses one fixed project.

## Known risks and limitations

- **Script length.** The LLM often writes dialogue that is too short for the target. Real runs needed one or two rewrite rounds, which costs GPU time. The prompt has not been tuned.
- **Machine stability.** Windows has hard-reset under GPU load and the laptop can sleep mid-job (`docs/assessment.md`, risk 1). Run real jobs on AC power with sleep off. Resume-or-fail is what limits the damage.
- **Orphan files.** A killed attempt leaves its files in its own `aN` directory. Nothing references them; they are not cleaned up.
- **Immutability is by convention.** Artifact files are never overwritten, but the database has no trigger that blocks UPDATE or DELETE on artifact rows.
- **Test scale.** Automated pipeline tests render 144×256 at 10 fps. The 1080×1920 at 30 fps path is checked only by real runs.
- **Error text.** Absolute paths are scrubbed from job errors on the way out; other content of stage errors is returned as stored.
- **Not tested:** the "LOCAL_ONLY task never reaches a cloud adapter" rule from CLAUDE.md has no literal test, because M1a has no such concept and no cloud adapter. Only the config refusal of non-loopback provider URLs is tested.
- **FFmpeg 8.0.1** predates a CVE fix in the MagicYUV decoder, which this project does not use (`docs/assessment.md`).
- Commits are local only; they have not been pushed.

## Next step

Close M1a, then start M1b with its consistency spike (IP-Adapter with reference sheets vs. per-character LoRA on this hardware: quality, VRAM, time per image) and wait for decision D8 before building. D9 (auth) must be settled before the app is reachable from anything but this machine, and D10 before M2.
