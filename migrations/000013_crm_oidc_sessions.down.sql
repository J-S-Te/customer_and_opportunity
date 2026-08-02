-- Development/empty-environment rollback only. Production rollback is forward-only:
-- disable the login entry, revoke sessions, retain records until the security retention window expires.
DROP TABLE IF EXISTS crm_machine_request_replays;
DROP TABLE IF EXISTS crm_oidc_sessions;
DROP TABLE IF EXISTS crm_oidc_login_transactions;
