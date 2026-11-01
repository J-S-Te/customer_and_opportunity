ALTER TABLE crm_oidc_login_transactions
    ADD COLUMN central_ticket_cipher VARBINARY(1024) NULL,
    ADD COLUMN force_login BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE crm_oidc_sessions
    ADD COLUMN refresh_token_cipher BLOB NULL,
    ADD COLUMN access_token_expires_at DATETIME(3) NULL,
    ADD COLUMN central_binding_cipher VARBINARY(1024) NULL,
    ADD COLUMN central_session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL;
