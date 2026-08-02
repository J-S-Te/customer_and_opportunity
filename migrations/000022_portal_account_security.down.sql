-- Destructive rollback: export portal_security_events first when its immutable
-- security timeline must be retained. All active sessions are revoked because
-- older code cannot address them through a safe browser-facing identifier.
UPDATE portal_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3));

DROP TABLE IF EXISTS portal_security_events;

ALTER TABLE portal_sessions
  DROP INDEX uq_portal_session_public_id,
  DROP COLUMN device_snapshot,
  DROP COLUMN location_snapshot,
  DROP COLUMN ip_masked,
  DROP COLUMN public_id;
