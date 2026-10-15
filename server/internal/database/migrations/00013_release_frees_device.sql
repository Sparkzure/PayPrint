-- +goose Up
-- Unbinding now hands the hardware back as a brand-new device.
--
-- Old semantics: a released row kept its fingerprint forever, so the device could only
-- come back through a login, never through a registration. New semantics: releasing a
-- device clears the pinned key, the ownership, the sessions and the replay ledger, and
-- gives the fingerprint back (the row is retired as status='released'). The next
-- registration or login on that hardware therefore starts from scratch.
--
-- Rows left in 'unbound' by the old code (and by migration 00011) still hold their
-- fingerprint. Finish their release here, so every "already unbound" device behaves the
-- same way. The row itself stays: orders and payment events reference it, and it keeps
-- the record of when and why the device was released.
--
-- The nonces have to go before the fingerprints are rotated: they are keyed by
-- fingerprint, not by device id.
DELETE FROM device_nonces
WHERE fingerprint IN (SELECT fingerprint FROM devices WHERE status = 'unbound');

DELETE FROM sessions
WHERE device_id IN (SELECT id FROM devices WHERE status = 'unbound');

UPDATE devices
SET status      = 'released',
    fingerprint = 'released:' || id,
    user_id     = NULL,
    public_key  = '',
    label       = '',
    platform    = '',
    app_version = '',
    bound_at    = 0,
    updated_at  = CAST(strftime('%s', 'now') AS INTEGER)
WHERE status = 'unbound';

-- +goose Down
-- Deliberately a no-op: which fingerprint was handed back is exactly the information
-- this migration destroys, so the previous claim cannot be reconstructed.
SELECT 1;
