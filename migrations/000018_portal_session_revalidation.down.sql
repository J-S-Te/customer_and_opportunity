-- Removing online authorization revalidation weakens session security. Revoke
-- all sessions before the controlled rollback so no browser continues with an
-- authorization snapshot that can no longer be checked.
UPDATE portal_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3));

ALTER TABLE portal_sessions
  DROP COLUMN authorization_checked_at,
  DROP COLUMN access_token_cipher;
