-- Older Portal versions cannot evaluate an online authorization-context data
-- scope snapshot. Revoke sessions before removing the column.
UPDATE portal_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3));

ALTER TABLE portal_sessions
  DROP COLUMN data_scopes_json;
