ALTER TABLE portal_activation_contexts
    ADD COLUMN central_ticket_cipher VARBINARY(1024) NULL;
ALTER TABLE portal_sessions
    ADD COLUMN refresh_token_cipher BLOB NULL,
    ADD COLUMN access_token_expires_at DATETIME(3) NULL,
    ADD COLUMN central_binding_cipher VARBINARY(1024) NULL,
    ADD COLUMN central_session_id VARCHAR(64) NULL;
