-- Development/empty-environment rollback only. Production rollback must retain the revoked
-- session evidence until the configured security retention window has elapsed.
ALTER TABLE crm_oidc_sessions
  DROP COLUMN organization_ids_json,
  DROP COLUMN primary_org_id;
