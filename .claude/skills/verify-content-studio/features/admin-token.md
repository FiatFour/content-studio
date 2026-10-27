# Admin token

Every action that changes data needs the admin token. The user pastes it into the header box; the API answers 401 without it.

## Sub-features

- `token-reject` a wrong token shows a visible alert.
- `token-accept` the right token closes the box.
- `token-api-guard` a mutating API call without a token returns 401.

## How to get to it (user POV)

- Press the `Admin token` button in the header, type the token into the `Admin token` field, press `Save token`.

## Driving it with drive.mjs

Preconditions:

- `doctor.sh $E` printed `doctor ok`.

- **Run it.** `node scripts/drive.mjs $E token`. It saves `definitely-wrong`, expects an alert with the `auth.rejected` text, then saves the real token and expects the field to disappear.
- **API guard.** The same run POSTs to `/api/episodes` with no `Authorization` header and expects status 401.

## Gotchas

- The real token lives in `$E` as `TOKEN`. Never print `$E` into a report or log.
- The browser keeps the token in local storage of that browser profile only. A fresh run starts with none.
