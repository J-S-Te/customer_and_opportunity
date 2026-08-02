-- Persist only the explicitly signed platform-to-PMS person binding. Existing sessions predate
-- this claim contract and are revoked so they cannot gain assignee capabilities without re-login.
ALTER TABLE crm_oidc_sessions
  ADD COLUMN person_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER platform_user_id;

UPDATE crm_oidc_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3));
