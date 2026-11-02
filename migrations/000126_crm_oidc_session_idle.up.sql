-- Target schema: CRM. Legacy business-subsystem idle policy after the published
-- 000124 central-session migration. Only explicit browser activity advances the
-- idle window; original absolute expiry and central binding remain unchanged.
-- Refresh-token ciphertext and access-token expiry already exist in 000124.
ALTER TABLE crm_oidc_sessions
  ADD COLUMN last_interactive_at DATETIME(3) NULL AFTER last_seen_at;

UPDATE crm_oidc_sessions
SET last_interactive_at = created_at
WHERE last_interactive_at IS NULL;
