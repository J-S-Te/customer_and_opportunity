DROP TABLE IF EXISTS portal_oidc_backchannel_logout_replays;
ALTER TABLE portal_sessions DROP KEY idx_portal_sessions_sid, DROP COLUMN oidc_sid;
