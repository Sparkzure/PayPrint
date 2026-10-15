-- +goose Up
-- The account is the identity. Orders, receipts and the audit trail hang off user_id;
-- a device is an attribute of the account, never the key other data depends on.
--
-- 1. order_audits -- the order journal the console previews and exports as CSV.
--
-- One immutable record per order, and deliberately denormalised: every field is a
-- snapshot taken when the order was placed. There are no foreign keys on purpose --
-- a site (and with it its orders) can be deleted from the console, an account can be
-- renamed, a device can be released or deleted, and none of that may change or erase
-- what the audit says happened.
CREATE TABLE order_audits (
    id                 TEXT    NOT NULL PRIMARY KEY,
    order_id           TEXT    NOT NULL,
    order_no           TEXT    NOT NULL,
    user_id            TEXT    NOT NULL,
    username           TEXT    NOT NULL DEFAULT '',
    device_label       TEXT    NOT NULL DEFAULT '',
    device_fingerprint TEXT    NOT NULL DEFAULT '',
    amount_fen         INTEGER NOT NULL,
    file_name          TEXT    NOT NULL DEFAULT '',
    color_mode         TEXT    NOT NULL DEFAULT '',
    duplex             TEXT    NOT NULL DEFAULT '',
    copies             INTEGER NOT NULL DEFAULT 0,
    total_pages        INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL
);

CREATE INDEX idx_order_audits_created ON order_audits (created_at);
CREATE INDEX idx_order_audits_user ON order_audits (user_id, created_at);

-- Backfill from the orders that already exist, so an upgrade does not start with an
-- empty audit trail. orders.device_id is still available here (it is dropped below),
-- which is what makes the device snapshot possible for historical rows.
--
-- total_pages follows the live rule: pages printed per copy times copies.
INSERT INTO order_audits (id, order_id, order_no, user_id, username, device_label,
                          device_fingerprint, amount_fen, file_name, color_mode, duplex,
                          copies, total_pages, created_at)
SELECT o.id,
       o.id,
       o.order_no,
       o.user_id,
       COALESCE(u.username, ''),
       COALESCE(d.label, ''),
       COALESCE(d.fingerprint, ''),
       o.amount_fen,
       COALESCE(f.original_name, ''),
       COALESCE(json_extract(o.params_json, '$.color_mode'), ''),
       COALESCE(json_extract(o.params_json, '$.duplex'), ''),
       COALESCE(json_extract(o.params_json, '$.copies'), 1),
       o.billable_sides * COALESCE(json_extract(o.params_json, '$.copies'), 1),
       o.created_at
FROM orders AS o
LEFT JOIN users AS u ON u.id = o.user_id
LEFT JOIN devices AS d ON d.id = o.device_id
LEFT JOIN print_files AS f ON f.id = o.file_id;

-- 2. Orders belong to the account. Which machine placed an order is no longer part of
-- the order: the audit row above keeps it as a snapshot, and nothing reads it back.
-- There is no index on the column, which is what lets SQLite drop it in place.
ALTER TABLE orders DROP COLUMN device_id;

-- 3. Receipts belong to the account as well: the account replaces the device.
ALTER TABLE payment_events DROP COLUMN device_id;
ALTER TABLE payment_events ADD COLUMN user_id TEXT NULL REFERENCES users (id);
CREATE INDEX idx_payment_events_user ON payment_events (user_id);

-- Existing receipts that were attributed to an order get the account of that order.
-- Receipts that never matched an order had no account to begin with and stay NULL.
UPDATE payment_events
SET user_id = (SELECT o.user_id FROM orders AS o WHERE o.id = payment_events.order_id)
WHERE order_id IS NOT NULL;

-- 4. The audit trail is the order journal now. The old audit_logs table carried 25
-- kinds of action records (logins, bans, device releases, settings changes, ...);
-- those actions keep their own trace on the rows they touch (ban_reason, revoke_reason,
-- ledger memo) and in the process log, and the table itself is retired with its data.
DROP TABLE audit_logs;

-- +goose Down
-- Shape only: which device placed an order, and which device reported a receipt, are
-- gone with the columns, so device_id comes back nullable and empty for existing rows.
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

DROP INDEX idx_payment_events_user;
ALTER TABLE payment_events DROP COLUMN user_id;
ALTER TABLE payment_events ADD COLUMN device_id TEXT NULL REFERENCES devices (id);

ALTER TABLE orders ADD COLUMN device_id TEXT NULL REFERENCES devices (id);

DROP TABLE order_audits;
