-- Display only; existing sessions remain valid and require a fresh login to obtain username.
ALTER TABLE crm_oidc_sessions ADD COLUMN username VARCHAR(255) NOT NULL DEFAULT '';
