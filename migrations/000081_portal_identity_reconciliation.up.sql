-- Target schema: CRM. Periodic reconciliation is read-only across the Portal
-- boundary; only durable run metrics and review findings are stored here.
CREATE TABLE crm_portal_identity_reconciliation_runs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  run_id VARCHAR(64) NOT NULL,
  worker_id VARCHAR(128) NOT NULL,
  status VARCHAR(16) NOT NULL,
  scanned_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  consistent_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  auto_compensation_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  needs_review_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  started_at DATETIME(3) NOT NULL,
  completed_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_identity_reconciliation_run (run_id),
  KEY idx_portal_identity_reconciliation_status (status, started_at),
  CONSTRAINT chk_portal_identity_reconciliation_run_status
    CHECK (status IN ('RUNNING','SUCCEEDED','FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_portal_identity_reconciliation_findings (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  finding_key CHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  crm_identity_link_id BIGINT UNSIGNED NOT NULL,
  finding_code VARCHAR(64) NOT NULL,
  resolution_mode VARCHAR(32) NOT NULL,
  status VARCHAR(16) NOT NULL,
  crm_status VARCHAR(16) NOT NULL,
  portal_status VARCHAR(16) NOT NULL DEFAULT '',
  customer_id BIGINT UNSIGNED NOT NULL,
  contact_id BIGINT UNSIGNED NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL,
  portal_account_id VARCHAR(64) NOT NULL DEFAULT '',
  compensation_status VARCHAR(16) NOT NULL DEFAULT '',
  first_detected_at DATETIME(3) NOT NULL,
  last_detected_at DATETIME(3) NOT NULL,
  occurrence_count BIGINT UNSIGNED NOT NULL DEFAULT 1,
  resolved_at DATETIME(3) NULL,
  last_run_id VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_identity_reconciliation_finding (finding_key),
  KEY idx_portal_identity_reconciliation_open (status, resolution_mode, last_detected_at),
  KEY idx_portal_identity_reconciliation_link (tenant_id, crm_identity_link_id, status),
  CONSTRAINT chk_portal_identity_reconciliation_finding_status
    CHECK (status IN ('OPEN','RESOLVED')),
  CONSTRAINT chk_portal_identity_reconciliation_resolution
    CHECK (resolution_mode IN ('AUTO_COMPENSATION','NEEDS_REVIEW')),
  CONSTRAINT fk_portal_identity_reconciliation_link
    FOREIGN KEY (crm_identity_link_id) REFERENCES crm_portal_identity_links (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
