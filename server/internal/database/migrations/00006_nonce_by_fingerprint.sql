-- +goose Up
-- Re-key nonce replay protection from device_id to the device fingerprint.
--
-- Device identity and the replay ledger are two different concerns. A request must
-- consume its nonce as soon as its signature verifies, whether or not the device
-- already exists. Keying on device_id made that impossible for a brand-new device:
-- there is no device row yet to satisfy the foreign key, so the nonce had to be
-- written later, inside the caller's registration/login transaction -- and when
-- that transaction never ran (username taken, wrong password, registration
-- closed) the nonce was never recorded at all, leaving the exact same signed
-- request replayable until the timestamp window closed.
--
-- The fingerprint is stable and unique per device, so the primary key keeps the
-- same guarantee: one (device, nonce) pair can be consumed exactly once.
--
-- SQLite cannot drop a foreign key in place, so the table is rebuilt.
CREATE TABLE device_nonces_new (
    fingerprint TEXT    NOT NULL,
    nonce       TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    PRIMARY KEY (fingerprint, nonce)
);

INSERT INTO device_nonces_new (fingerprint, nonce, created_at, expires_at)
SELECT d.fingerprint, n.nonce, n.created_at, n.expires_at
FROM device_nonces AS n
JOIN devices AS d ON d.id = n.device_id;

DROP TABLE device_nonces;
ALTER TABLE device_nonces_new RENAME TO device_nonces;

CREATE INDEX idx_device_nonces_expires ON device_nonces (expires_at);

-- +goose Down
CREATE TABLE device_nonces_old (
    device_id  TEXT    NOT NULL REFERENCES devices (id),
    nonce      TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (device_id, nonce)
);

INSERT INTO device_nonces_old (device_id, nonce, created_at, expires_at)
SELECT d.id, n.nonce, n.created_at, n.expires_at
FROM device_nonces AS n
JOIN devices AS d ON d.fingerprint = n.fingerprint;

DROP TABLE device_nonces;
ALTER TABLE device_nonces_old RENAME TO device_nonces;

CREATE INDEX idx_device_nonces_expires ON device_nonces (expires_at);
