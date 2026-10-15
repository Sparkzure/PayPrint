-- +goose Up
-- Admin sessions carry no device.
--
-- An admin is a person, not a machine: they must be able to sign in from a laptop,
-- a phone, or the print point itself. Binding the session to a device fingerprint
-- would lock them out the moment they switch machines, and it would drag the whole
-- device-key apparatus (TOFU pinning, "one device, one account for life") into a
-- flow that has nothing to do with printing.
--
-- device_id therefore becomes nullable: consumer sessions still fill it in, admin
-- sessions leave it NULL. SQLite cannot relax a NOT NULL constraint in place, so
-- the table is rebuilt (same idiom as migration 00006).
CREATE TABLE sessions_new (
    id           TEXT    NOT NULL PRIMARY KEY,
    user_id      TEXT    NOT NULL REFERENCES users (id),
    device_id    TEXT    NULL REFERENCES devices (id),
    token_hash   TEXT    NOT NULL UNIQUE,
    issued_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    revoked_at   INTEGER NOT NULL DEFAULT 0,
    last_used_at INTEGER NOT NULL
);

INSERT INTO sessions_new (id, user_id, device_id, token_hash, issued_at, expires_at,
                          revoked_at, last_used_at)
SELECT id, user_id, device_id, token_hash, issued_at, expires_at, revoked_at, last_used_at
FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX idx_sessions_user ON sessions (user_id);
CREATE INDEX idx_sessions_expires ON sessions (expires_at);
CREATE INDEX idx_sessions_device ON sessions (device_id);

-- Collecting payments is an API credential now, not a device flag.
--
-- Modelling the listener as a user account meant it needed a login, a password, a
-- device binding and a role -- four things it never uses -- and it merged "who may
-- print" with "who may collect" into one table. A credential is the honest shape:
-- one row, one site, one token.
--
-- Only the SHA-256 of the token is stored, so a database leak cannot be replayed as
-- a listener. The token is shown exactly once, when it is created.
CREATE TABLE collector_credentials (
    id            TEXT    NOT NULL PRIMARY KEY,
    printer_id    TEXT    NOT NULL REFERENCES printers (id),
    label         TEXT    NOT NULL DEFAULT '',
    token_hash    TEXT    NOT NULL UNIQUE,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER NOT NULL DEFAULT 0,
    revoked_at    INTEGER NOT NULL DEFAULT 0,
    revoke_reason TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_collector_credentials_printer ON collector_credentials (printer_id);

-- The device-side collector flag is superseded by the table above. The index has to
-- go first: SQLite refuses to drop an indexed column.
DROP INDEX idx_devices_collector;
ALTER TABLE devices DROP COLUMN collector_printer_id;

-- The user system has exactly two roles from here on (admin / user). Any leftover
-- operator row is turned into an ordinary user rather than left holding a role no
-- code understands.
UPDATE users SET role = 'user' WHERE role = 'operator';

-- +goose Down
-- The role rewrite above is intentionally not undone: nothing records which users
-- used to be operators, and inventing an operator role back would hand those rows a
-- permission model that no longer exists. Down is for the schema only.
ALTER TABLE devices ADD COLUMN collector_printer_id TEXT NULL REFERENCES printers (id);
CREATE INDEX idx_devices_collector ON devices (collector_printer_id);

DROP TABLE collector_credentials;

CREATE TABLE sessions_old (
    id           TEXT    NOT NULL PRIMARY KEY,
    user_id      TEXT    NOT NULL REFERENCES users (id),
    device_id    TEXT    NOT NULL REFERENCES devices (id),
    token_hash   TEXT    NOT NULL UNIQUE,
    issued_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    revoked_at   INTEGER NOT NULL DEFAULT 0,
    last_used_at INTEGER NOT NULL
);

INSERT INTO sessions_old (id, user_id, device_id, token_hash, issued_at, expires_at,
                          revoked_at, last_used_at)
SELECT id, user_id, device_id, token_hash, issued_at, expires_at, revoked_at, last_used_at
FROM sessions
WHERE device_id IS NOT NULL;

DROP TABLE sessions;
ALTER TABLE sessions_old RENAME TO sessions;

CREATE INDEX idx_sessions_user ON sessions (user_id);
CREATE INDEX idx_sessions_expires ON sessions (expires_at);
