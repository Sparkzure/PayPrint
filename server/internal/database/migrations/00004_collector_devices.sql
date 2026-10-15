-- +goose Up
-- Collector devices ("payment listener").
--
-- The payment listener is NOT a consumer client. It runs on the device that is
-- logged into the merchant's WeChat account and reports receipts on behalf of
-- every user ordering at that site. A device is a collector exactly when
-- collector_printer_id is set, so "flagged as a collector" and "bound to a site"
-- can never drift apart, and every report made by it is scoped to one site.
--
-- Deliberately no separate boolean: two columns could disagree, one cannot.
ALTER TABLE devices ADD COLUMN collector_printer_id TEXT NULL REFERENCES printers (id);

CREATE INDEX idx_devices_collector ON devices (collector_printer_id);

-- Amount matching access path: a WeChat receipt only carries an amount, so the
-- target order is looked up by (site, exact amount) among orders that can still
-- absorb the payment.
CREATE INDEX idx_orders_match ON orders (printer_id, amount_fen, status);

-- +goose Down
DROP INDEX idx_orders_match;
DROP INDEX idx_devices_collector;
ALTER TABLE devices DROP COLUMN collector_printer_id;
