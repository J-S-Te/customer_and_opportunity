-- customer_portal schema only. Apply after 000024_portal_feedback.up.sql.
CREATE TABLE portal_service_evaluations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL,
  evaluation_no VARCHAR(48) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  project_id VARCHAR(64) NOT NULL,
  professional_score TINYINT UNSIGNED NOT NULL,
  response_score TINYINT UNSIGNED NOT NULL,
  report_score TINYINT UNSIGNED NOT NULL,
  attitude_score TINYINT UNSIGNED NOT NULL,
  total_score TINYINT UNSIGNED NOT NULL,
  average_score DECIMAL(3,2) NOT NULL,
  comment TEXT NOT NULL,
  status VARCHAR(24) NOT NULL,
  submitted_at DATETIME(3) NOT NULL,
  create_idempotency_key VARCHAR(128) NOT NULL,
  create_request_hash CHAR(64) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_evaluation_public_id (public_id),
  UNIQUE KEY uq_portal_evaluation_no (tenant_id,evaluation_no),
  UNIQUE KEY uq_portal_evaluation_project (tenant_id,customer_id,project_id),
  UNIQUE KEY uq_portal_evaluation_idempotency (tenant_id,customer_id,account_id,create_idempotency_key),
  KEY idx_portal_evaluation_account (tenant_id,customer_id,account_id,submitted_at),
  KEY idx_portal_evaluation_statistics (tenant_id,status,submitted_at),
  CONSTRAINT chk_portal_evaluation_professional CHECK (professional_score BETWEEN 1 AND 5),
  CONSTRAINT chk_portal_evaluation_response CHECK (response_score BETWEEN 1 AND 5),
  CONSTRAINT chk_portal_evaluation_report CHECK (report_score BETWEEN 1 AND 5),
  CONSTRAINT chk_portal_evaluation_attitude CHECK (attitude_score BETWEEN 1 AND 5),
  CONSTRAINT chk_portal_evaluation_total CHECK (total_score BETWEEN 4 AND 20),
  CONSTRAINT chk_portal_evaluation_average CHECK (average_score BETWEEN 1.00 AND 5.00),
  CONSTRAINT chk_portal_evaluation_status CHECK (status IN ('SUBMITTED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_evaluation_audit_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  evaluation_id BIGINT UNSIGNED NOT NULL,
  action VARCHAR(32) NOT NULL,
  actor_id VARCHAR(128) NOT NULL,
  request_id VARCHAR(64) NOT NULL DEFAULT '',
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_portal_evaluation_audit (tenant_id,evaluation_id,occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_evaluation_alerts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  evaluation_id BIGINT UNSIGNED NOT NULL,
  rule_code VARCHAR(48) NOT NULL,
  status VARCHAR(16) NOT NULL,
  triggered_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_evaluation_alert (tenant_id,evaluation_id,rule_code),
  KEY idx_portal_evaluation_alert_status (tenant_id,status,triggered_at),
  CONSTRAINT chk_portal_evaluation_alert_status CHECK (status IN ('TRIGGERED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_evaluation_notifications (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  evaluation_id BIGINT UNSIGNED NOT NULL,
  kind VARCHAR(48) NOT NULL,
  status VARCHAR(16) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  read_at DATETIME(3) NULL,
  read_by VARCHAR(128) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_evaluation_notice (tenant_id,evaluation_id,kind),
  KEY idx_portal_evaluation_notice_status (tenant_id,status,created_at),
  CONSTRAINT chk_portal_evaluation_notice_status CHECK (status IN ('UNREAD','READ'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_evaluation_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  aggregate_id BIGINT UNSIGNED NOT NULL,
  payload JSON NOT NULL,
  status VARCHAR(16) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  sent_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_evaluation_event (event_id),
  KEY idx_portal_evaluation_outbox (tenant_id,status,created_at),
  CONSTRAINT chk_portal_evaluation_outbox_status CHECK (status IN ('PENDING','SENT','CANCELLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
