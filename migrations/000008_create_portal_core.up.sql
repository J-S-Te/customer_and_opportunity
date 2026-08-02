CREATE TABLE portal_identity_links (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  account_no VARCHAR(32) NOT NULL, platform_user_id VARCHAR(128) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL, contact_id BIGINT UNSIGNED NULL,
  status VARCHAR(16) NOT NULL, display_name VARCHAR(200) NOT NULL DEFAULT '',
  activated_at DATETIME(3) NULL, disabled_at DATETIME(3) NULL,
  last_claims_revision BIGINT UNSIGNED NOT NULL DEFAULT 0, last_verified_at DATETIME(3) NULL,
  created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id), UNIQUE KEY uq_portal_account_no (tenant_id, account_no),
  UNIQUE KEY uq_portal_platform_user (tenant_id, platform_user_id),
  KEY idx_portal_identity_customer (tenant_id, customer_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_sessions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  session_id_hash VARCHAR(64) NOT NULL, platform_user_id VARCHAR(128) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL, authz_revision BIGINT UNSIGNED NOT NULL,
  role_config_hash VARCHAR(128) NOT NULL, expires_at DATETIME(3) NOT NULL,
  absolute_expiry DATETIME(3) NOT NULL, last_seen_at DATETIME(3) NOT NULL,
  revoked_at DATETIME(3) NULL, ip_hash VARCHAR(64) NOT NULL DEFAULT '',
  user_agent_hash VARCHAR(64) NOT NULL DEFAULT '', created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL, deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY (id),
  UNIQUE KEY uq_portal_session_hash (session_id_hash),
  KEY idx_portal_session_subject (tenant_id, platform_user_id, revoked_at, expires_at),
  KEY idx_portal_session_customer (tenant_id, customer_id, revoked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_activation_contexts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  context_id_hash VARCHAR(64) NOT NULL, invite_token_hash VARCHAR(64) NOT NULL,
  invite_token_cipher VARBINARY(1024) NULL, expected_platform_user_id VARCHAR(128) NOT NULL DEFAULT '',
  customer_id BIGINT UNSIGNED NOT NULL DEFAULT 0, state_hash VARCHAR(64) NOT NULL,
  nonce_hash VARCHAR(64) NOT NULL, nonce_cipher VARBINARY(1024) NOT NULL,
  pkce_verifier_cipher VARBINARY(1024) NOT NULL, return_path VARCHAR(500) NOT NULL,
  expires_at DATETIME(3) NOT NULL, consumed_at DATETIME(3) NULL,
  created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id), UNIQUE KEY uq_portal_activation_context (context_id_hash),
  UNIQUE KEY uq_portal_activation_state (state_hash),
  KEY idx_portal_activation_expiry (tenant_id, expires_at, consumed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_auth_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL DEFAULT '', customer_id BIGINT UNSIGNED NULL,
  type VARCHAR(64) NOT NULL, result VARCHAR(16) NOT NULL, reason_code VARCHAR(64) NOT NULL DEFAULT '',
  request_id VARCHAR(64) NOT NULL, occurred_at DATETIME(3) NOT NULL, PRIMARY KEY (id),
  KEY idx_portal_auth_subject (tenant_id, platform_user_id, occurred_at),
  KEY idx_portal_auth_request (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_snapshots (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  project_id VARCHAR(64) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL,
  project_name VARCHAR(200) NOT NULL, contract_no VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL, progress_pct TINYINT UNSIGNED NOT NULL,
  current_stage VARCHAR(64) NOT NULL, expected_end_date DATE NULL, `delayed` BOOLEAN NOT NULL,
  manager_name VARCHAR(128) NOT NULL DEFAULT '', manager_contact_masked VARCHAR(128) NOT NULL DEFAULT '',
  source_updated_at DATETIME(3) NOT NULL, synced_at DATETIME(3) NOT NULL,
  raw_version VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL, deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project (tenant_id, customer_id, project_id),
  KEY idx_portal_projects_customer (tenant_id, customer_id, status, source_updated_at),
  CONSTRAINT chk_portal_project_progress CHECK (progress_pct <= 100)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_milestones (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL, project_id VARCHAR(64) NOT NULL,
  stage_code VARCHAR(64) NOT NULL, stage_name VARCHAR(128) NOT NULL,
  status VARCHAR(32) NOT NULL, planned_at DATETIME(3) NULL, completed_at DATETIME(3) NULL,
  sort_no INT NOT NULL, PRIMARY KEY (id),
  KEY idx_portal_milestone_customer (tenant_id, customer_id, project_id, sort_no)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_activities (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL, project_id VARCHAR(64) NOT NULL,
  source_activity_id VARCHAR(64) NOT NULL, type VARCHAR(32) NOT NULL,
  content TEXT NOT NULL, occurred_at DATETIME(3) NOT NULL, PRIMARY KEY (id),
  UNIQUE KEY uq_portal_activity_source (tenant_id, customer_id, project_id, source_activity_id),
  KEY idx_portal_activity_customer (tenant_id, customer_id, project_id, occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_team (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL, project_id VARCHAR(64) NOT NULL,
  person_ref VARCHAR(64) NOT NULL, name VARCHAR(128) NOT NULL, role VARCHAR(64) NOT NULL,
  contact_masked VARCHAR(128) NOT NULL DEFAULT '', PRIMARY KEY (id),
  KEY idx_portal_team_customer (tenant_id, customer_id, project_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_report_requests (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  request_no VARCHAR(32) NOT NULL, project_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL, account_id VARCHAR(128) NOT NULL,
  report_type VARCHAR(64) NOT NULL, reason VARCHAR(2000) NOT NULL,
  receive_email_cipher VARBINARY(1024) NULL, status VARCHAR(32) NOT NULL,
  downstream_request_id VARCHAR(128) NOT NULL DEFAULT '', approval_result VARCHAR(2000) NOT NULL DEFAULT '',
  submitted_at DATETIME(3) NOT NULL, approved_at DATETIME(3) NULL, issued_at DATETIME(3) NULL,
  idempotency_key VARCHAR(128) NOT NULL, request_hash VARCHAR(64) NOT NULL,
  last_callback_version BIGINT UNSIGNED NOT NULL DEFAULT 0, created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL, deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_no (tenant_id, request_no),
  UNIQUE KEY uq_portal_report_idempotency (tenant_id, customer_id, idempotency_key),
  KEY idx_portal_report_customer (tenant_id, customer_id, status, submitted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_report_files (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL, object_key_cipher VARBINARY(1024) NOT NULL,
  file_name VARCHAR(255) NOT NULL, mime VARCHAR(128) NOT NULL, size BIGINT NOT NULL,
  file_hash VARCHAR(128) NOT NULL, encryption_key_ref VARCHAR(255) NOT NULL,
  watermark_status VARCHAR(32) NOT NULL, created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL, deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_file (tenant_id, request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_report_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, event_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL, event_type VARCHAR(64) NOT NULL,
  aggregate_id BIGINT UNSIGNED NOT NULL, payload JSON NOT NULL,
  status VARCHAR(16) NOT NULL, created_at DATETIME(3) NOT NULL, PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_event (event_id),
  KEY idx_portal_report_outbox (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- The Portal is a separate deployment and schema. Apply this migration only
-- to the customer_portal database; sharing the CRM schema is unsupported.
