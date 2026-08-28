DROP TABLE IF EXISTS crm_oidc_backchannel_logout_replays;
ALTER TABLE crm_oidc_sessions DROP KEY idx_crm_oidc_sessions_sid, DROP COLUMN oidc_sid;
