# Shell and language

The app opens in Thai, lists live pages as links, marks later-milestone pages as inert "Not implemented" items, and stores the chosen language in the backend so it survives a reload.

## Sub-features

- `shell-default-th` opens in Thai.
- `shell-inert-nav` shows AI agent tower (M4), Providers & budget (M2) and Library (M3) as non-links tagged `Mx · <Not implemented>`.
- `shell-lang-switch` switches to English with the `EN` button.
- `shell-lang-persist` keeps English after a reload, from the backend setting.

## How to get to it (user POV)

- Open the app root `$URL/`.
- Press `TH` or `EN` in the header's `Language` group.

## Driving it with drive.mjs

Preconditions:

- `doctor.sh $E` printed `doctor ok`.

- **Default Thai.** `node scripts/drive.mjs $E shell`. The navigation shows the Thai `menu.episodes` link.
- **Inert items.** The same run asserts three items are not links and show their tag plus the Thai "Not implemented" text.
- **Switch and persist.** The run presses `EN`, reloads, and reads `GET /api/settings`: `ui_lang` is `en`.

## Gotchas

- The tag text is `M4 · Not implemented` as one string. Asserting only `M4` fails.
- The header machine chips (CPU/RAM/VRAM) read `n/a` until the first poll returns (about 2.5 s). A screenshot taken straight after load shows `n/a`; later ones show values. Do not assert on the chips.
- Thai text in monospace elements needs a Thai-capable fallback font. Headless Chromium has no system Thai font, so before the fix in `web/src/index.css` those labels rendered as boxes. If Thai shows as boxes in a screenshot, check the font stack before blaming the string.
