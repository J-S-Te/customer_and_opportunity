ALTER TABLE crm_oidc_sessions
    ADD COLUMN oidc_sid VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER platform_user_id,
    ADD KEY idx_crm_oidc_sessions_sid (oidc_sid);

CREATE TABLE crm_oidc_backchannel_logout_replays (
    jti VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    issuer VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    expires_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (jti),
    KEY idx_crm_logout_replay_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
