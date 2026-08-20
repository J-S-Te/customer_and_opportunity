-- Target schema: CRM. Store only the validated base-platform login IP in the
-- server-side session; browser cookies remain opaque. Historical sessions are
-- compatible with an empty value and require no inferred backfill.
ALTER TABLE crm_oidc_sessions
  ADD COLUMN login_ip VARCHAR(45) NOT NULL DEFAULT '' AFTER display_name;
