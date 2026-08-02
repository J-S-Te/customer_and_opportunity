-- Apply only to the independent customer_portal database. This state table
-- coordinates incremental customer-scoped project snapshot pulls.
ALTER TABLE portal_project_snapshots
  CHANGE COLUMN manager_name manager_name_snapshot VARCHAR(128) NOT NULL DEFAULT '';

CREATE TABLE portal_project_sync_states (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  sync_cursor VARCHAR(1024) NOT NULL DEFAULT '',
  next_run_at DATETIME(3) NOT NULL,
  last_attempt_at DATETIME(3) NULL,
  last_success_at DATETIME(3) NULL,
  last_error_summary VARCHAR(1000) NOT NULL DEFAULT '',
  locked_by VARCHAR(128) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_sync_customer (tenant_id, customer_id),
  KEY idx_portal_project_sync_claim (tenant_id, next_run_at, locked_until, customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing active mappings become due immediately; INSERT IGNORE also makes
-- this migration safe if a pre-deploy backfill already created a state row.
INSERT IGNORE INTO portal_project_sync_states
  (tenant_id, customer_id, sync_cursor, next_run_at, last_error_summary, locked_by,
   created_at, updated_at, version)
SELECT tenant_id, customer_id, '', UTC_TIMESTAMP(3), '', '',
       UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 1
FROM portal_identity_links
WHERE status = 'ACTIVE' AND deleted_at IS NULL
GROUP BY tenant_id, customer_id;
