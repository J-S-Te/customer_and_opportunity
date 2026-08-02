-- Controlled empty/test-environment rollback only. Before shrinking, prove no
-- stored actor identifier exceeds 64 bytes; otherwise this rollback is unsafe.
ALTER TABLE portal_report_grants
  MODIFY COLUMN created_by VARCHAR(64) NOT NULL,
  MODIFY COLUMN updated_by VARCHAR(64) NOT NULL;

ALTER TABLE portal_report_requests
  MODIFY COLUMN created_by VARCHAR(64) NOT NULL,
  MODIFY COLUMN updated_by VARCHAR(64) NOT NULL;
