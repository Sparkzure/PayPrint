-- +goose Up
-- Users. username_ci is the case-folded username used for the global unique constraint.
CREATE TABLE users (
    id            TEXT    NOT NULL PRIMARY KEY,
    username      TEXT    NOT NULL,
    username_ci   TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    phone         TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL DEFAULT 'user',
    status        TEXT    NOT NULL DEFAULT 'active',
    balance_fen   INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- Devices.
-- Hard rule: a device can be bound to exactly one account for its whole lifetime.
-- The UNIQUE constraint on fingerprint is the last line of defence against a
-- concurrent registration race (INSERT conflict == device already taken).
-- public_key holds the non-exportable ECDSA-P256 key generated inside the Android
-- Keystore (base64 SPKI DER). It is pinned on first contact and every later request
-- must be signed by it.
CREATE TABLE devices (
    id             TEXT    NOT NULL PRIMARY KEY,
    fingerprint    TEXT    NOT NULL UNIQUE,
    public_key     TEXT    NOT NULL,
    public_key_alg TEXT    NOT NULL DEFAULT 'ecdsa-p256',
    status         TEXT    NOT NULL DEFAULT 'active',
    user_id        TEXT    NULL REFERENCES users (id),
    bound_at       INTEGER NOT NULL DEFAULT 0,
    label          TEXT    NOT NULL DEFAULT '',
    platform       TEXT    NOT NULL DEFAULT '',
    app_version    TEXT    NOT NULL DEFAULT '',
    first_seen_at  INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL,
    revoked_at     INTEGER NOT NULL DEFAULT 0,
    revoke_reason  TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE INDEX idx_devices_user ON devices (user_id);

-- Nonce replay protection. The (device_id, nonce) primary key makes a replayed
-- request fail at INSERT time instead of silently succeeding.
CREATE TABLE device_nonces (
    device_id  TEXT    NOT NULL REFERENCES devices (id),
    nonce      TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (device_id, nonce)
);

CREATE INDEX idx_device_nonces_expires ON device_nonces (expires_at);

-- Sessions. Only the SHA-256 of the bearer token is stored, so a database leak
-- cannot be replayed as a login.
CREATE TABLE sessions (
    id           TEXT    NOT NULL PRIMARY KEY,
    user_id      TEXT    NOT NULL REFERENCES users (id),
    device_id    TEXT    NOT NULL REFERENCES devices (id),
    token_hash   TEXT    NOT NULL UNIQUE,
    issued_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    revoked_at   INTEGER NOT NULL DEFAULT 0,
    last_used_at INTEGER NOT NULL
);

CREATE INDEX idx_sessions_user ON sessions (user_id);
CREATE INDEX idx_sessions_expires ON sessions (expires_at);

-- Audit trail. Manual unbind, account bans and manual compensation must be traceable.
CREATE TABLE audit_logs (
    id          TEXT    NOT NULL PRIMARY KEY,
    actor_type  TEXT    NOT NULL,
    actor_id    TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    target_type TEXT    NOT NULL DEFAULT '',
    target_id   TEXT    NOT NULL DEFAULT '',
    detail      TEXT    NOT NULL DEFAULT '',
    ip          TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_audit_logs_created ON audit_logs (created_at);

-- +goose Down
DROP TABLE audit_logs;
DROP TABLE sessions;
DROP TABLE device_nonces;
DROP TABLE devices;
DROP TABLE users;
