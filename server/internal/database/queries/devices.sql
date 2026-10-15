-- name: CreateDevice :one
INSERT INTO devices (
    id, fingerprint, public_key, public_key_alg, status, user_id, bound_at,
    label, platform, app_version, first_seen_at, last_seen_at, created_at, updated_at
) VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetDeviceByID :one
SELECT * FROM devices WHERE id = ?;

-- A released row is not a device any more: its fingerprint was handed back, so the
-- lookup must not see it -- the same hardware has to come back as a brand-new device.
-- name: GetDeviceByFingerprint :one
SELECT * FROM devices
WHERE fingerprint = ?
  AND status <> 'released';

-- Binds only while user_id is still NULL. The affected-row count is the second
-- line of defence: 0 means another account grabbed the device concurrently.
-- public_key is written here as well because a row can be owner-less *and* pin-less:
-- that is the legacy 'unbound' state (old releases, migration 00011/00013 leftovers),
-- where the login that binds the device also re-pins the key it presents. A device
-- released under the *new* semantics never reaches this statement: its fingerprint was
-- handed back, so the login creates a brand-new row instead.
-- name: BindDevice :execrows
UPDATE devices
SET user_id = ?, bound_at = ?, public_key = ?, last_seen_at = ?, updated_at = ?
WHERE id = ? AND user_id IS NULL;

-- name: TouchDevice :exec
UPDATE devices
SET last_seen_at = ?, platform = ?, app_version = ?, updated_at = ?
WHERE id = ?;

-- Manual unbind ("release"): hand the hardware back as a brand-new device.
--
-- Everything that gave this row control over the hardware is wiped:
--   * user_id      -- no owner
--   * public_key   -- no pinned key (after a factory reset the Keystore generates a new
--                     key, and keeping the old pin would brick the hardware forever)
--   * fingerprint  -- handed back, so the next registration/login on that hardware sees
--                     no device at all and starts from scratch
--   * label / platform / app_version / bound_at -- device description, also residue
--
-- The row itself stays on purpose, but it is no longer a device: it is retired as
-- 'released' and only survives as the history anchor that orders and payment events
-- reference (they cannot point at a deleted row). Sessions and nonces are removed by
-- the caller in the same transaction (DeleteSessionsByDevice / DeleteNoncesByFingerprint).
--
-- No payment-collection permission to clear any more: collecting is a credential
-- (see collector_credentials), not a property of a device, so releasing the hardware
-- cannot leave a site-wide collection grant behind.
--
-- Zero affected rows means the row was already released; the caller reports a conflict.
-- name: ReleaseDevice :execrows
UPDATE devices
SET status        = 'released',
    fingerprint   = 'released:' || id,
    user_id       = NULL,
    public_key    = '',
    label         = '',
    platform      = '',
    app_version   = '',
    bound_at      = 0,
    revoked_at    = ?,
    revoke_reason = ?,
    updated_at    = ?
WHERE id = ?
  AND status <> 'released';

-- Suspend a device *without* dropping its binding.
--
-- Unlike ReleaseDevice this deliberately keeps user_id and public_key: suspension
-- is meant to be reversible ("restore"), so whatever is needed to put the device
-- back exactly where it was has to survive. A suspended device is still refused
-- everywhere, because every auth path requires status = 'active'.
--
-- A released row is refused as well: it is not a device any more, and suspending it
-- would drag a retired tombstone back into the device list (and 'restore' would then
-- turn it into a live 'unbound' row with a bogus fingerprint).
--
-- Zero affected rows means it is already suspended or was just released; the caller
-- reports a conflict.
-- name: BlockDevice :execrows
UPDATE devices
SET status = 'revoked',
    revoked_at = ?,
    revoke_reason = ?,
    updated_at = ?
WHERE id = ?
  AND status != 'revoked'
  AND status != 'released';

-- Lift a suspension.
--
-- The target status is chosen by the caller: a device that still has an owner
-- goes back to 'active' (its binding and pinned key were never touched), while a
-- legacy row suspended before bindings were preserved has no owner left and can only
-- go back to 'unbound' (logging in re-binds it, and it still holds its fingerprint).
--
-- A released row can never be restored: its fingerprint is gone, so the status guard
-- below (only 'revoked') is what keeps it out -- releasing is not reversible, the
-- hardware comes back as a brand-new device instead.
--
-- Zero affected rows means it is not suspended (or was restored concurrently).
-- name: RestoreDevice :execrows
UPDATE devices
SET status = ?,
    revoked_at = 0,
    revoke_reason = '',
    updated_at = ?
WHERE id = ?
  AND status = 'revoked';

-- name: ListDevicesByUser :many
SELECT * FROM devices WHERE user_id = ? ORDER BY created_at DESC;

-- Released rows are not listed: they are no longer devices, and after an unbind the
-- operator should not see a fingerprint-less shell next to the real devices. What the
-- release did is recorded in the audit log.
-- name: ListAllDevices :many
SELECT * FROM devices
WHERE status <> 'released'
ORDER BY created_at DESC LIMIT ?;
