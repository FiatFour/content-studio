# Remote access

The default is local only: the server binds `127.0.0.1:8080` and you open <http://localhost:8080>. Nothing here is needed for that. Read this only if you want to reach the studio from another device. Background and the controls behind it: `docs/spec/security-ops.md`, "Authentication" and "Transport security".

Rules the server enforces:

- A non-loopback `STUDIO_ADDR` (`0.0.0.0`, a LAN address, a hostname) is refused at startup unless `STUDIO_TLS_CERT` and `STUDIO_TLS_KEY` are set. `STUDIO_ALLOW_INSECURE_REMOTE=true` overrides this; it serves passkey logins and the session cookie over plain http and logs a warning. Do not use it outside a network you fully trust.
- Passkeys (WebAuthn) only work from a secure context: `https://` or `http://localhost`. A plain `http://192.168.x.x` page cannot create or use a passkey at all, so the override does not even give you a working login from another device.
- There is no built-in certificate issuance, renewal or reload.

## Settings that must agree

| Setting | What to set for `https://studio.example.ts.net` |
|---|---|
| `STUDIO_AUTH_RP_ID` | the bare host name, `studio.example.ts.net` (no scheme, no port) |
| `STUDIO_AUTH_ORIGINS` | `https://studio.example.ts.net` (comma separated for more; no path; plain `http` is accepted only for localhost) |
| `STUDIO_ADDR` | stays `127.0.0.1:8080` when a proxy terminates TLS |
| Session cookie | becomes `Secure` automatically: the request is TLS, or its `Origin` is a configured `https://` origin |
| HSTS | sent automatically when the request is TLS or its `Host` equals the host of a configured `https://` origin |

Passkeys are bound to one RP ID. A passkey created for `localhost` does not work for `studio.example.ts.net` and the reverse, and the server accepts one RP ID at a time. So switching the RP ID means: stop the server, delete the old credentials (`DELETE FROM auth_credentials;` in the database, which also ends every session), set the new RP ID and origins, start the server, run `go run ./cmd/server enroll` and enroll again from the new address (`enroll` prints the first configured origin). You can keep `http://localhost:8080` in `STUDIO_AUTH_ORIGINS` for the Origin check, but local passkey login stops working while the RP ID is the remote name. Plan for that before you switch, and keep shell access to the database for recovery.

## Option A: Tailscale serve (recommended)

Keeps the server on loopback, reachable only from your tailnet, with a real certificate that Tailscale issues and renews.

1. Enable HTTPS certificates for your tailnet in the Tailscale admin console.
2. Start the studio unchanged on `127.0.0.1:8080`, with `STUDIO_AUTH_RP_ID` and `STUDIO_AUTH_ORIGINS` set as in the table above.
3. `tailscale serve --bg 8080` (the exact syntax differs by Tailscale version; check `tailscale serve --help`). Do not use `tailscale funnel`: that publishes to the whole internet.
4. Open `https://studio.example.ts.net` from a device on the tailnet, enroll, sign in.

Notes: the proxy connects from `127.0.0.1`, so the server sees one client address; the anonymous rate limit and the login-failure limit are then shared by everyone who can reach the proxy. Tailscale ACLs decide who that is; keep the list short.

## Option B: reverse proxy you run (Caddy, nginx)

1. The proxy terminates TLS with a certificate for your host name and forwards to `http://127.0.0.1:8080`, passing the original `Host` header and `Origin` unchanged.
2. Do not expose port 8080 itself. Keep `STUDIO_ADDR=127.0.0.1:8080`.
3. Set RP ID and origins as above. Do not add `X-Forwarded-*` based settings; the server ignores them on purpose.
4. Do not strip or rewrite the security headers the server sends. If the proxy adds its own `Content-Security-Policy`, make sure it is not looser than the server's.
5. Request bodies are capped by the server (1 MiB by default, less on most routes); proxy limits only need to be at least that. Allow range requests and long responses on `/api/artifacts/*/content`, because the video player streams from there.

## Option C: the server terminates TLS itself

For a certificate you already have (for example from your own CA or `mkcert`): set `STUDIO_TLS_CERT=/path/cert.pem` and `STUDIO_TLS_KEY=/path/key.pem` (both required, readable by the server user, key mode 600) and `STUDIO_ADDR` to the address to listen on. The server logs `tls: true` at startup. TLS 1.2 or newer. Renewing the certificate needs a restart. A self-signed certificate makes browsers refuse passkeys unless the device trusts it, so use a CA the device trusts.

## What this does not give you

- No protection against someone who already holds the passkey or a signed-in browser.
- No per-device sessions, IP binding, or alerts; sessions last up to 7 days (`STUDIO_SESSION_MAX`), idle 12 h (`STUDIO_SESSION_IDLE`).
- Rate limits are in memory and per process (see the spec).
- The AI services (Ollama, ComfyUI, the voice service) must stay on loopback; the server refuses provider URLs that are not loopback.
- Not verified here: a real remote deployment with Tailscale or Caddy. HSTS and `Secure` cookie logic are covered by tests (a test TLS server and a simulated proxy `Host`). The `STUDIO_TLS_*` listener was started once by hand with a self-signed certificate and answered over HTTPS with HSTS; there is no automated test for that listener.
