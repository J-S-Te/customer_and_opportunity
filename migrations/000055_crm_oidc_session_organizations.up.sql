-- CRM schema only. Persist the signed direct-membership organization snapshot used when the
-- server-side session was created. Existing sessions predate this authorization contract and
-- are revoked so they must complete a fresh OIDC login before accessing CRM data.
ALTER TABLE crm_oidc_sessions
  ADD COLUMN primary_org_id VARCHAR(64) NOT NULL DEFAULT '' AFTER display_name,
  ADD COLUMN organization_ids_json JSON NULL AFTER primary_org_id;

UPDATE crm_oidc_sessions
SET organization_ids_json = JSON_ARRAY(),
    revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3))
WHERE organization_ids_json IS NULL;

ALTER TABLE crm_oidc_sessions
  MODIFY COLUMN organization_ids_json JSON NOT NULL;
