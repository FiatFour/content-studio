# Create an episode

A user fills the episode form and starts a run. They land on the episode page, and the first stage waits in the queue. In the isolated instance no worker runs, so `queued` is the true end state.

## Sub-features

- `create-form` title, idea and target length fields on the Episodes page.
- `create-land` redirect to `#/episodes/<id>`.
- `create-queued` first stage `Script` is `queued`.
- `create-listed` the episode appears in the list after a reload.

## How to get to it (user POV)

- Open `$URL/#/episodes`, set the token, fill `Episode title`, `Idea` and `Target length (seconds)`, press `Start episode`.

## Driving it with drive.mjs

Preconditions:

- `doctor.sh $E` printed `doctor ok`. No worker is running against this database.

- **Run it.** `node scripts/drive.mjs $E create-episode`.
- **UI proof.** The URL matches `#/episodes/<number>`, the page shows `Script` and `queued`, and the list link with the typed title exists after `goto #/episodes`.
- **Stored proof.** `GET /api/episodes` returns the row with `latest_run.state` `running` and `jobs[0]` `script` / `queued`.

## Gotchas

- Do not start a worker against this database. It would call the real Ollama/ComfyUI and use the GPU. Use the user's live stack for pipeline checks instead.
- The idea must be at least 10 characters and the length 60 to 120. The backend returns 400 otherwise.
- The form needs the token set first (`auth.missing` message otherwise).
