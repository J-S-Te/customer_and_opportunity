-- customer_portal schema only. ISSUED callbacks enqueue encrypted descriptors;
-- external object retrieval, scanning and encryption never execute while a
-- report request row lock or callback transaction is held.
CREATE TABLE portal_report_ingest_jobs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  descriptor_cipher VARBINARY(2048) NOT NULL,
  descriptor_hash VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  retry_count TINYINT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3) NULL,
  locked_by VARCHAR(128) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  last_error_summary VARCHAR(1000) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  completed_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_ingest_event (event_id),
  UNIQUE KEY uq_portal_report_ingest_request (tenant_id, request_id),
  KEY idx_portal_report_ingest_lease (status, locked_until, next_retry_at, created_at),
  CONSTRAINT fk_portal_report_ingest_request
    FOREIGN KEY (tenant_id, customer_id, request_id)
    REFERENCES portal_report_requests (tenant_id, customer_id, id),
  CONSTRAINT chk_portal_report_ingest_status CHECK
    (status IN ('PENDING','PROCESSING','RETRY_WAIT','COMPLETED','DEAD_LETTER')),
  CONSTRAINT chk_portal_report_ingest_completion CHECK
    ((status='COMPLETED' AND completed_at IS NOT NULL AND locked_by='' AND locked_until IS NULL)
      OR (status<>'COMPLETED' AND completed_at IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
