-- customer_portal schema only.
-- Append-only status events are created empty. There is deliberately no
-- synthetic history backfill: inventing actor, trace or event time for old
-- requests would make the audit trail misleading.
ALTER TABLE portal_report_requests
  ADD UNIQUE KEY uq_portal_report_scope_id (tenant_id, customer_id, id);

CREATE TABLE portal_report_status_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  sequence BIGINT UNSIGNED NOT NULL,
  from_status VARCHAR(32) NOT NULL DEFAULT '',
  to_status VARCHAR(32) NOT NULL,
  actor_type VARCHAR(32) NOT NULL,
  actor_id VARCHAR(128) NOT NULL DEFAULT '',
  source_key_hash VARCHAR(64) NOT NULL,
  payload_hash VARCHAR(64) NOT NULL DEFAULT '',
  request_trace VARCHAR(128) NOT NULL DEFAULT '',
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_status_source
    (tenant_id, request_id, source_key_hash),
  UNIQUE KEY uq_portal_report_status_sequence
    (tenant_id, request_id, sequence),
  KEY idx_portal_report_status_timeline
    (tenant_id, customer_id, request_id, sequence, id),
  CONSTRAINT fk_portal_report_status_request
    FOREIGN KEY (tenant_id, customer_id, request_id)
    REFERENCES portal_report_requests (tenant_id, customer_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
