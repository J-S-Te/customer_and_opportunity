-- Target schema: CRM. Unified business-subsystem session policy: idle timeout slides only on
-- explicit browser activity, absolute timeout never slides. Existing sessions keep their current
-- absolute expiry; NULL last_interactive_at is backfilled from created_at so an upgrade does not
-- immediately lock every online session. The refresh-token ciphertext and access-token expiry let
-- the server refresh the OIDC access token before its short lifetime ends; legacy rows keep both
-- columns NULL and retain their pre-upgrade revalidation behaviour until they expire or the user
-- signs in again. Sensitive token ciphertext follows the existing key-rotation/data-retention runbook.
ALTER TABLE crm_oidc_sessions
  ADD COLUMN last_interactive_at DATETIME(3) NULL AFTER last_seen_at;

ALTER TABLE crm_oidc_sessions
  ADD COLUMN refresh_token_cipher BLOB NULL AFTER access_token_cipher;

ALTER TABLE crm_oidc_sessions
  ADD COLUMN access_token_expires_at DATETIME(3) NULL AFTER refresh_token_cipher;

UPDATE crm_oidc_sessions
SET last_interactive_at = created_at
WHERE last_interactive_at IS NULL;
