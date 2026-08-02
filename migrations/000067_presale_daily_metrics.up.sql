CREATE TABLE crm_presale_daily_metrics (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  metric_date DATE NOT NULL,
  organization_id VARCHAR(64) NOT NULL DEFAULT '',
  person_id VARCHAR(64) NOT NULL,
  person_name_snapshot VARCHAR(128) NOT NULL,
  department_snapshot VARCHAR(128) NOT NULL DEFAULT '',
  opportunity_id BIGINT UNSIGNED NOT NULL,
  work_hours DECIMAL(18,2) NOT NULL DEFAULT 0.00,
  request_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  worklog_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  pms_outbox_worklog_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  pms_success_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  source_max_updated_at DATETIME(3) NOT NULL,
  aggregated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_presale_daily_metric_dimension
    (tenant_id,metric_date,organization_id,person_id,opportunity_id),
  KEY idx_presale_daily_metric_tenant_date (tenant_id,metric_date),
  KEY idx_presale_daily_metric_org_date (tenant_id,organization_id,metric_date),
  KEY idx_presale_daily_metric_person_date (tenant_id,person_id,metric_date),
  KEY idx_presale_daily_metric_opportunity_date (tenant_id,opportunity_id,metric_date),
  CONSTRAINT chk_presale_daily_metric_hours CHECK (work_hours >= 0),
  CONSTRAINT chk_presale_daily_metric_pms CHECK (pms_success_count <= pms_outbox_worklog_count)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_daily_metric_runs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  window_start DATE NOT NULL,
  window_end_exclusive DATE NOT NULL,
  status VARCHAR(16) NOT NULL,
  worker_id VARCHAR(128) NOT NULL,
  row_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  source_max_updated_at DATETIME(3) NULL,
  started_at DATETIME(3) NOT NULL,
  finished_at DATETIME(3) NULL,
  error_summary VARCHAR(1000) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_presale_daily_metric_run (tenant_id,started_at),
  CONSTRAINT chk_presale_daily_metric_run_window CHECK (window_end_exclusive > window_start),
  CONSTRAINT chk_presale_daily_metric_run_status CHECK (status IN ('RUNNING','SUCCESS','FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- The aggregate is a rebuildable projection. It deliberately has no foreign
-- keys so retention of the source business evidence is independent from the
-- reporting projection. The worker replaces one tenant/window transactionally;
-- it never treats a partial range as current.
