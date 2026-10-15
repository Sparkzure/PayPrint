-- +goose Up
-- Printer catalogue. cups_uri points at a queue on the remote CUPS gateway.
CREATE TABLE printers (
    id              TEXT    NOT NULL PRIMARY KEY,
    code            TEXT    NOT NULL UNIQUE,
    name            TEXT    NOT NULL,
    cups_uri        TEXT    NOT NULL,
    location        TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'online',
    price_bw_fen    INTEGER NOT NULL DEFAULT 0,
    price_color_fen INTEGER NOT NULL DEFAULT 0,
    supports_color  INTEGER NOT NULL DEFAULT 0,
    supports_duplex INTEGER NOT NULL DEFAULT 0,
    paper_sizes     TEXT    NOT NULL DEFAULT '["A4"]',
    max_copies      INTEGER NOT NULL DEFAULT 99,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX idx_printers_sort ON printers (sort_order, code);

-- Uploaded documents waiting to be printed. storage_path is server-local and is
-- never exposed to clients.
CREATE TABLE print_files (
    id            TEXT    NOT NULL PRIMARY KEY,
    user_id       TEXT    NOT NULL REFERENCES users (id),
    original_name TEXT    NOT NULL,
    content_type  TEXT    NOT NULL DEFAULT 'application/pdf',
    size_bytes    INTEGER NOT NULL,
    sha256        TEXT    NOT NULL,
    storage_path  TEXT    NOT NULL,
    page_count    INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
);

CREATE INDEX idx_print_files_user ON print_files (user_id, created_at);

-- Orders. All money is integer fen, never floating point.
-- params_json / cost_json are snapshots taken at checkout so that a later price
-- change can never mutate the meaning of a historical order.
CREATE TABLE orders (
    id              TEXT    NOT NULL PRIMARY KEY,
    order_no        TEXT    NOT NULL UNIQUE,
    user_id         TEXT    NOT NULL REFERENCES users (id),
    device_id       TEXT    NOT NULL REFERENCES devices (id),
    printer_id      TEXT    NOT NULL REFERENCES printers (id),
    file_id         TEXT    NOT NULL REFERENCES print_files (id),
    status          TEXT    NOT NULL,
    amount_fen      INTEGER NOT NULL,
    currency        TEXT    NOT NULL DEFAULT 'CNY',
    title           TEXT    NOT NULL DEFAULT '',
    params_json     TEXT    NOT NULL,
    cost_json       TEXT    NOT NULL,
    billable_sides  INTEGER NOT NULL,
    sheets_per_copy INTEGER NOT NULL,
    is_test         INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    paid_at         INTEGER NOT NULL DEFAULT 0,
    completed_at    INTEGER NOT NULL DEFAULT 0,
    closed_at       INTEGER NOT NULL DEFAULT 0,
    close_reason    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_orders_user ON orders (user_id, created_at);
CREATE INDEX idx_orders_status ON orders (status, expires_at);

-- Print dispatch queue. order_id is UNIQUE, so one paid order can never produce
-- two CUPS jobs even if the payment report is delivered twice.
-- "Paid but submission failed" is a first class path: attempts + next_retry_at
-- drive the retry loop.
CREATE TABLE print_jobs (
    id            TEXT    NOT NULL PRIMARY KEY,
    order_id      TEXT    NOT NULL UNIQUE REFERENCES orders (id),
    printer_id    TEXT    NOT NULL REFERENCES printers (id),
    status        TEXT    NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL DEFAULT 5,
    cups_job_id   INTEGER NOT NULL DEFAULT 0,
    ipp_job_uri   TEXT    NOT NULL DEFAULT '',
    job_state     TEXT    NOT NULL DEFAULT '',
    last_error    TEXT    NOT NULL DEFAULT '',
    next_retry_at INTEGER NOT NULL,
    locked_at     INTEGER NOT NULL DEFAULT 0,
    locked_by     TEXT    NOT NULL DEFAULT '',
    submitted_at  INTEGER NOT NULL DEFAULT 0,
    finished_at   INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE INDEX idx_print_jobs_dispatch ON print_jobs (status, next_retry_at);

-- +goose Down
DROP TABLE print_jobs;
DROP TABLE orders;
DROP TABLE print_files;
DROP TABLE printers;
