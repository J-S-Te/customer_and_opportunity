-- Portal report revision ledger. Historical files and download events remain immutable; a newly
-- issued revision revokes grants bound to the superseded revision.
ALTER TABLE portal_report_requests
  ADD COLUMN current_report_revision INT UNSIGNED NOT NULL DEFAULT 0 AFTER last_callback_hash,
  ADD COLUMN report_validity_status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE' AFTER current_report_revision,
  ADD COLUMN void_notice VARCHAR(1000) NOT NULL DEFAULT '' AFTER report_validity_status;

ALTER TABLE portal_report_files
  DROP INDEX uq_portal_report_file,
  ADD COLUMN report_revision INT UNSIGNED NOT NULL DEFAULT 0 AFTER request_id,
  ADD COLUMN validity_status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE' AFTER report_revision,
  ADD COLUMN void_notice VARCHAR(1000) NOT NULL DEFAULT '' AFTER validity_status,
  ADD UNIQUE KEY uq_portal_report_file_revision (tenant_id, request_id, report_revision),
  ADD KEY idx_portal_report_file_current (tenant_id, request_id, validity_status, report_revision);

ALTER TABLE portal_report_ingest_jobs
  DROP INDEX uq_portal_report_ingest_request,
  ADD COLUMN report_revision INT UNSIGNED NOT NULL DEFAULT 0 AFTER request_id,
  ADD UNIQUE KEY uq_portal_report_ingest_revision (tenant_id, request_id, report_revision);

ALTER TABLE portal_report_grants
  ADD COLUMN report_revision INT UNSIGNED NOT NULL DEFAULT 0 AFTER request_id,
  ADD KEY idx_portal_report_grant_revision (tenant_id, request_id, report_revision, status);

CREATE TABLE portal_report_revision_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  report_revision INT UNSIGNED NOT NULL,
  event_type VARCHAR(32) NOT NULL,
  reason VARCHAR(1000) NOT NULL DEFAULT '',
  source_key_hash CHAR(64) NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_revision_event (tenant_id, request_id, report_revision, event_type),
  KEY idx_portal_report_revision_timeline (tenant_id, customer_id, request_id, report_revision, occurred_at),
  CONSTRAINT chk_portal_report_revision_event CHECK (event_type IN ('ISSUED','VOIDED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
