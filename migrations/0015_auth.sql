-- Passkey (WebAuthn) authentication for the single owner (D9, decided 2026-10-29).
-- Secrets: a session token is never stored, only its SHA-256; an enrollment code likewise.
-- Credentials hold public keys only.

-- One row: the stable, random WebAuthn user handle of the owner (not personal data).
CREATE TABLE auth_owner (
    id          smallint PRIMARY KEY CHECK (id = 1),
    user_handle bytea NOT NULL CHECK (length(user_handle) = 32),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_credentials (
    id               bigserial PRIMARY KEY,
    credential_id    bytea NOT NULL UNIQUE CHECK (length(credential_id) BETWEEN 1 AND 1023),
    public_key       bytea NOT NULL,
    sign_count       bigint NOT NULL DEFAULT 0 CHECK (sign_count >= 0),
    transports       text[] NOT NULL DEFAULT '{}',
    flags            smallint NOT NULL DEFAULT 0,
    attestation_type text NOT NULL DEFAULT '',
    aaguid           bytea,
    clone_warning    boolean NOT NULL DEFAULT false,
    label            text NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_used_at     timestamptz
);

-- credential_id is NULL only for kind 'test': sessions minted by the throwaway-database helper
-- (scripts/testsession) and by tests. No HTTP path creates one.
CREATE TABLE auth_sessions (
    id            bigserial PRIMARY KEY,
    token_hash    bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    credential_id bigint REFERENCES auth_credentials (id) ON DELETE CASCADE,
    kind          text NOT NULL DEFAULT 'passkey' CHECK (kind IN ('passkey', 'test')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    CHECK (kind = 'test' OR credential_id IS NOT NULL)
);

-- One-time codes that allow the FIRST passkey to be registered. Created only by `server enroll`.
CREATE TABLE auth_enroll_codes (
    id         bigserial PRIMARY KEY,
    code_hash  bytea NOT NULL UNIQUE CHECK (length(code_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
