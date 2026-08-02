-- Revoke all active Portal sessions before rollback because old code cannot
-- enforce permissions once these validated snapshots are removed.
UPDATE portal_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3));

ALTER TABLE portal_sessions
  DROP COLUMN permissions_json,
  DROP COLUMN roles_json;
