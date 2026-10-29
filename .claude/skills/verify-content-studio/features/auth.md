# Passkey sign-in

The app is behind a passkey (D9). Logged out, the user sees the enrollment screen (no passkey yet) or the sign-in screen. After sign-in they get the app; they can sign out, add and remove passkeys on the Security page, and any 401 drops them back to the login screen.

## Sub-features

- `auth-enroll` first passkey with the one-time code from `server enroll`; a wrong code is refused.
- `auth-session` session cookie is HttpOnly and SameSite=Strict, survives a reload, is revoked on sign out.
- `auth-login` sign in again with the passkey.
- `auth-security` add a passkey, remove one, the last one cannot be removed.
- `auth-expired` a session revoked on the server returns the app to the login screen.

## How to get to it (user POV)

- Open `http://localhost:$PORT` (NOT `127.0.0.1`: a WebAuthn RP ID must be a domain name, and `localhost` counts as a secure context over plain http).

## Driving it with drive.mjs

Preconditions:

- `doctor.sh $E` printed `doctor ok`, and the instance is FRESH (no passkey yet). The flow enrolls one, so run it once per `launch.sh`; for any other flow use another instance.
- Headless Chromium with a CDP virtual authenticator (`WebAuthn.addVirtualAuthenticator`); no real device is involved.

- **Run it.** `node scripts/drive.mjs $E auth`. It starts logged out (it does not use the test session), runs `$DIR/server enroll` against the instance's own database to get the code, and keeps the code in memory only.
- **UI proof.** Screenshots `1-enroll-screen` to `5-session-expired` in the evidence folder, plus assertions on the visible headings, buttons and messages.
- **Stored proof.** `psql` reads on the instance's `verify_*` database: credential count, the code absent in plaintext and marked used, `revoked_at` after sign out, the `auth.login.success` audit row.

## Gotchas

- Chromium allows one `internal` virtual authenticator, so the "second device" is a `usb` authenticator added after the first is removed. Adding a passkey from the same authenticator is refused by design (exclude list) and the UI shows the duplicate message.
- The screens are Thai (default) in this flow; strings come from `th.json`.
- This verifies the app against a software authenticator, not a hardware key or a platform authenticator such as Windows Hello.
