-- Apply only to the independent customer_portal database. Authorization data
-- scopes are calculated by the basic platform and cached only in the
-- server-side Portal session; browser cookies remain opaque.
ALTER TABLE portal_sessions
  ADD COLUMN data_scopes_json JSON NULL AFTER permissions_json;

-- Sessions created by older versions have no verifiable scope snapshot. Do
-- not silently upgrade them: revoke and require a fresh OIDC login.
UPDATE portal_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3)),
    data_scopes_json = JSON_ARRAY()
WHERE data_scopes_json IS NULL;

ALTER TABLE portal_sessions
  MODIFY COLUMN data_scopes_json JSON NOT NULL;
