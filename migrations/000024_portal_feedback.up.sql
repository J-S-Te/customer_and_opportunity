-- customer_portal schema only. Apply before enabling feedback routes or worker.
CREATE TABLE portal_feedbacks (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL, feedback_no VARCHAR(48) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL, account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  project_id VARCHAR(64) NOT NULL DEFAULT '', type VARCHAR(24) NOT NULL,
  title VARCHAR(200) NOT NULL, description TEXT NOT NULL,
  expected_contact_cipher VARBINARY(1024) NOT NULL, expected_contact_masked VARCHAR(200) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL, reject_reason VARCHAR(1000) NOT NULL DEFAULT '',
  submitted_at DATETIME(3) NOT NULL, first_response_due_at DATETIME(3) NOT NULL,
  first_responded_at DATETIME(3) NULL, resolved_at DATETIME(3) NULL, closed_at DATETIME(3) NULL,
  create_idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL, create_request_hash VARCHAR(64) NOT NULL,
  created_by VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL, updated_by VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id), UNIQUE KEY uq_portal_feedback_public_id (public_id),
  UNIQUE KEY uq_portal_feedback_no (tenant_id,feedback_no),
  UNIQUE KEY uq_portal_feedback_create (tenant_id,customer_id,account_id,create_idempotency_key),
  KEY idx_portal_feedback_customer (tenant_id,customer_id,account_id,submitted_at),
  KEY idx_portal_feedback_sla (status,first_response_due_at,first_responded_at),
  CONSTRAINT chk_portal_feedback_type CHECK (type IN ('OBJECTION','COMPLAINT','SUGGESTION')),
  CONSTRAINT chk_portal_feedback_status CHECK (status IN ('SUBMITTED','ACCEPTED','PROCESSING','NEED_CUSTOMER_INFO','RESOLVED','CLOSED','REJECTED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_feedback_messages (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  feedback_id BIGINT UNSIGNED NOT NULL, sender_type VARCHAR(16) NOT NULL,
  sender_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL, content TEXT NOT NULL, visibility VARCHAR(16) NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL, request_hash VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL, PRIMARY KEY (id),
  UNIQUE KEY uq_portal_feedback_message_idempotency (tenant_id,feedback_id,sender_type,sender_id,idempotency_key),
  KEY idx_portal_feedback_message_timeline (tenant_id,feedback_id,created_at),
  CONSTRAINT chk_portal_feedback_sender CHECK (sender_type IN ('CUSTOMER','OPERATOR')),
  CONSTRAINT chk_portal_feedback_visibility CHECK (visibility IN ('CUSTOMER','INTERNAL'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_feedback_status_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  feedback_id BIGINT UNSIGNED NOT NULL, from_status VARCHAR(32) NOT NULL,
  to_status VARCHAR(32) NOT NULL, reason VARCHAR(1000) NOT NULL DEFAULT '',
  actor_type VARCHAR(16) NOT NULL, actor_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  request_id VARCHAR(64) NOT NULL DEFAULT '', idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NULL,
  request_hash VARCHAR(64) NOT NULL DEFAULT '', occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), KEY idx_portal_feedback_status_timeline (tenant_id,feedback_id,occurred_at),
  -- A nullable key keeps initial/system logs unconstrained while every keyed
  -- status action is unique across the tenant and therefore conflicts across
  -- feedbacks or actors instead of silently executing twice.
  UNIQUE KEY uq_portal_feedback_status_action (tenant_id,idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_feedback_escalations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  feedback_id BIGINT UNSIGNED NOT NULL, level TINYINT UNSIGNED NOT NULL,
  reason VARCHAR(64) NOT NULL, sent_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_portal_feedback_escalation (tenant_id,feedback_id,level),
  KEY idx_portal_feedback_escalation_time (tenant_id,sent_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_feedback_notifications (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  feedback_id BIGINT UNSIGNED NOT NULL, kind VARCHAR(48) NOT NULL,
  status VARCHAR(16) NOT NULL, created_at DATETIME(3) NOT NULL, read_at DATETIME(3) NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_portal_feedback_notice (feedback_id,kind),
  KEY idx_portal_feedback_notice_pending (tenant_id,status,created_at),
  CONSTRAINT chk_portal_feedback_notice_status CHECK (status IN ('UNREAD','READ'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_feedback_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, event_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL, event_type VARCHAR(64) NOT NULL,
  aggregate_id BIGINT UNSIGNED NOT NULL, payload JSON NOT NULL,
  status VARCHAR(16) NOT NULL, created_at DATETIME(3) NOT NULL, sent_at DATETIME(3) NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_portal_feedback_event (event_id),
  KEY idx_portal_feedback_outbox_pending (tenant_id,status,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_feedback_job_leases (
  job_name VARCHAR(64) NOT NULL, owner_id VARCHAR(128) NOT NULL,
  lease_until DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (job_name), KEY idx_portal_feedback_job_lease (lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Attachment metadata is deferred until trusted upload, malware scanning and
-- object storage exist. Unscanned files therefore cannot become visible.
