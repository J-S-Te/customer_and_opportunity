-- CRM-only migration. Portal uses a different schema and independent cookies/OAuth client.
CREATE TABLE crm_oidc_login_transactions (
  state_hash VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  nonce_cipher VARBINARY(512) NOT NULL,
  code_verifier_cipher VARBINARY(512) NOT NULL,
  return_path VARCHAR(500) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (state_hash),
  KEY idx_crm_oidc_login_expiry (expires_at),
  KEY idx_crm_oidc_login_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE crm_oidc_sessions (
  session_id_hash VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL,
  display_name VARCHAR(200) NOT NULL DEFAULT '',
  roles_json JSON NOT NULL,
  permissions_json JSON NOT NULL,
  role_config_hash VARCHAR(128) NOT NULL,
  authz_revision BIGINT UNSIGNED NOT NULL,
  access_token_cipher BLOB NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  authorization_checked_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  last_seen_at DATETIME(3) NOT NULL,
  revoked_at DATETIME(3) NULL,
  PRIMARY KEY (session_id_hash),
  KEY idx_crm_oidc_session_tenant_user (tenant_id, platform_user_id),
  KEY idx_crm_oidc_session_expiry (expires_at),
  KEY idx_crm_oidc_session_revoked (revoked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE crm_machine_request_replays (
  replay_hash VARCHAR(64) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (replay_hash),
  KEY idx_crm_machine_replay_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Release order: run this additive migration before deploying production OIDC mode.
-- Sensitive access-token ciphertext must be destroyed through a key-rotation/data-retention runbook.
