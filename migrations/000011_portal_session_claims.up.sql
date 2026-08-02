-- Apply only to the independent customer_portal database. Opaque browser
-- cookies contain no claims; the server persists this validated authorization
-- snapshot with the local session.
ALTER TABLE portal_sessions
  ADD COLUMN roles_json JSON NULL AFTER role_config_hash,
  ADD COLUMN permissions_json JSON NULL AFTER roles_json;

-- Existing sessions were created without a validated authorization snapshot.
-- They cannot be upgraded safely, so revoke them and require a fresh OIDC login.
UPDATE portal_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3)),
    roles_json = JSON_ARRAY(),
    permissions_json = JSON_ARRAY()
WHERE roles_json IS NULL OR permissions_json IS NULL;

ALTER TABLE portal_sessions
  MODIFY COLUMN roles_json JSON NOT NULL,
  MODIFY COLUMN permissions_json JSON NOT NULL;
