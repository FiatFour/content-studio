# Review, approve and export

A reviewer opens a video that passed QA, checks the hash, approves or rejects it, and exports exactly the approved file.

## Sub-features

- `review-queue` `Needs your review` lists videos with `qa_passed`.
- `review-approve` `Approve` is enabled only for a QA-passed version; the decision stores the hash the reviewer saw.
- `review-notes` `Request changes` and `Reject` require notes.
- `review-export` `Export approved file` copies the approved file after re-checking its hash.

## How to get to it (user POV)

- Open `$URL/#/review`, then a video, or follow `Open review` on an episode's version list.

## Driving it with drive.mjs

NOT driven yet. There is no flow for this feature. The checks below are what a flow must prove once a QA-passed video exists.

Preconditions:

- A video with `validation_status = qa_passed` exists. The isolated instance has none, because it has no worker. Use the live stack, or add a seeded fixture video, which is not built.

- **Approve.** Press `Approve` and confirm. Expect `Approved`, and a row in `approvals` whose sha256 equals the file's `sha256sum`.
- **Export.** Press `Export approved file`. Expect `episode-<n>-v<k>-<hash12>.mp4` in the export dir and its sha256 equal to the approved hash.
- **Re-render.** `Re-render` makes a new, unapproved version.

## Gotchas

- As of 2026-10-27 no real episode has reached `ready_for_review`, so this path has only been proven by Go tests with fake providers.
- Approving writes an approval row. Do this only against a throwaway database, never the user's live stack, unless the user asks.
