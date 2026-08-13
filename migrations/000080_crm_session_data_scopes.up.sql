ALTER TABLE crm_oidc_sessions
  ADD COLUMN data_scopes_json JSON NULL AFTER permissions_json;

UPDATE crm_oidc_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3)),
    data_scopes_json = JSON_ARRAY()
WHERE data_scopes_json IS NULL;

ALTER TABLE crm_oidc_sessions
  MODIFY COLUMN data_scopes_json JSON NOT NULL;
