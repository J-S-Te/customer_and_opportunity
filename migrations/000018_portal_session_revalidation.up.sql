-- Apply only to the independent customer_portal database. Existing sessions
-- have no recoverable access token and must reauthenticate before UserInfo
-- revalidation can be enforced.
ALTER TABLE portal_sessions
  ADD COLUMN access_token_cipher BLOB NULL AFTER permissions_json,
  ADD COLUMN authorization_checked_at DATETIME(3) NULL AFTER access_token_cipher;

UPDATE portal_sessions
SET revoked_at = COALESCE(revoked_at, UTC_TIMESTAMP(3)),
    access_token_cipher = X'',
    authorization_checked_at = COALESCE(last_seen_at, created_at)
WHERE access_token_cipher IS NULL OR authorization_checked_at IS NULL;

ALTER TABLE portal_sessions
  MODIFY COLUMN access_token_cipher BLOB NOT NULL,
  MODIFY COLUMN authorization_checked_at DATETIME(3) NOT NULL;
