-- +goose Up
-- Reported payment events.
-- event_id is UNIQUE, so re-delivering the same event returns the original result
-- (idempotency). Nothing in this row is trusted: it is evidence of "what was
-- claimed", never proof that the money arrived.
CREATE TABLE payment_events (
    id            TEXT    NOT NULL PRIMARY KEY,
    event_id      TEXT    NOT NULL UNIQUE,
    source        TEXT    NOT NULL,
    channel       TEXT    NOT NULL,
    order_id      TEXT    NULL REFERENCES orders (id),
    order_no      TEXT    NOT NULL,
    device_id     TEXT    NULL REFERENCES devices (id),
    out_trade_no  TEXT    NOT NULL DEFAULT '',
    amount_fen    INTEGER NOT NULL,
    status        TEXT    NOT NULL,
    reject_reason TEXT    NOT NULL DEFAULT '',
    reported_at   INTEGER NOT NULL,
    client_ts     INTEGER NOT NULL,
    nonce         TEXT    NOT NULL DEFAULT '',
    raw_payload   TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);

CREATE INDEX idx_payment_events_order ON payment_events (order_id);

-- Ledger. payment entries are positive, refund entries negative; balance_fen is the
-- post-entry running total for the user. Written in the SAME transaction that
-- releases the print job, so "released but not accounted" cannot happen.
CREATE TABLE ledger_entries (
    id          TEXT    NOT NULL PRIMARY KEY,
    order_id    TEXT    NOT NULL REFERENCES orders (id),
    user_id     TEXT    NOT NULL REFERENCES users (id),
    entry_type  TEXT    NOT NULL,
    amount_fen  INTEGER NOT NULL,
    balance_fen INTEGER NOT NULL,
    channel     TEXT    NOT NULL DEFAULT '',
    memo        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_ledger_order_type ON ledger_entries (order_id, entry_type);
CREATE INDEX idx_ledger_user ON ledger_entries (user_id, created_at);

-- Fallback order query against the official payment provider API.
-- A device-side report is only a trigger; the official query is the authority.
CREATE TABLE order_verifications (
    id            TEXT    NOT NULL PRIMARY KEY,
    order_id      TEXT    NOT NULL REFERENCES orders (id),
    channel       TEXT    NOT NULL,
    out_trade_no  TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL DEFAULT 5,
    last_error    TEXT    NOT NULL DEFAULT '',
    result        TEXT    NOT NULL DEFAULT '',
    next_retry_at INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_order_verifications_order ON order_verifications (order_id);
CREATE INDEX idx_order_verifications_dispatch ON order_verifications (status, next_retry_at);

-- +goose Down
DROP TABLE order_verifications;
DROP TABLE ledger_entries;
DROP TABLE payment_events;
