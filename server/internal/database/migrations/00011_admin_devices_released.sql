-- +goose Up
-- Admin accounts must not occupy a device.
--
-- An admin signs in through the console, and that path never creates or uses a
-- device row (see migration 00007: an admin session leaves sessions.device_id
-- NULL). The one path that used to hand an admin account a device binding was
-- bootstrap: on an empty database the very first registration becomes an admin,
-- and it created a devices row exactly like a member registration would.
--
-- That row consumed the fingerprint. devices.fingerprint is UNIQUE, so the machine
-- the operator used to bootstrap the service could never carry a member account
-- again: registering returned device_already_used, and logging in returned
-- device_bound_to_other_account. Bootstrap locked the operator's own machine out of
-- the consumer side.
--
-- Fix: release the device rows owned by admin accounts, with the same semantics as
-- a manual admin release (status=unbound, user_id NULL, key pin cleared so the next
-- login on that hardware re-pins whatever key the device presents). The row itself
-- stays: it is the lifetime record of the fingerprint, and a released device still
-- can never be used for a new *registration*, only for a login.
--
-- Admins keep working after this: their console sessions carry no device_id, and
-- any consumer session they held dies with the change below, because
-- AuthenticateSession rejects a device that is not active or not owned by the user.
--
-- New installs never reach this state: AuthService.Register no longer creates a
-- device row for the bootstrap admin.
UPDATE devices
SET status        = 'unbound',
    user_id       = NULL,
    public_key    = '',
    revoked_at    = CAST(strftime('%s', 'now') AS INTEGER),
    revoke_reason = 'admin accounts no longer occupy a device (migration 00011)',
    updated_at    = CAST(strftime('%s', 'now') AS INTEGER)
WHERE user_id IN (SELECT id FROM users WHERE role = 'admin');

-- +goose Down
-- Deliberately a no-op: which admin owned which fingerprint is exactly the
-- information this migration destroys, so the previous binding cannot be
-- reconstructed. Releasing a device is also not something that can be undone in
-- general -- the pin it clears can only be re-established by the hardware itself.
SELECT 1;
