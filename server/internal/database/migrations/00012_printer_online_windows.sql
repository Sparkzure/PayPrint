-- +goose Up
-- Per-printer weekly online windows.
--
-- A self-service print point is not open around the clock: the printer may sit in
-- a shop that closes, or behind a counter nobody staffs at night. Until now the
-- only way to take a site out of service was flipping status to offline/disabled
-- by hand -- forgetting to flip it back was the failure mode.
--
-- online_windows holds a JSON array of windows, empty array = always online
-- (which is what every existing row gets, so this migration changes no behaviour):
--
--   [{"days":[1,2,3,4,5],"start":"08:00","end":"22:00"}]
--
-- days are ISO weekdays (Monday=1 .. Sunday=7). end earlier than start means the
-- window runs past midnight (e.g. 22:00-02:00 on days [5,6]). Evaluated against
-- the server's local time; the value is validated on write (see
-- service.ValidateOnlineWindows) and enforced on order creation, on release and
-- on dispatch -- a printer outside its windows counts as offline.
ALTER TABLE printers ADD COLUMN online_windows TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE printers DROP COLUMN online_windows;
