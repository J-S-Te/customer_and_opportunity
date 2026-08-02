-- customer_portal schema only. Apply after 000038 and before enabling report
-- grant routes. No grant or audit history is synthesized for old reports.
CREATE TABLE portal_report_grants (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  issue_key_hash VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  active_slot VARCHAR(8) NULL,
  expires_at DATETIME(3) NOT NULL,
  download_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  risk_state VARCHAR(32) NOT NULL DEFAULT '',
  last_download_at DATETIME(3) NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_grant_public (tenant_id, public_id),
  UNIQUE KEY uq_portal_report_grant_token (token_hash),
  UNIQUE KEY uq_portal_report_grant_issue_key
    (tenant_id, customer_id, request_id, account_id, issue_key_hash),
  -- MySQL permits multiple NULL values. ACTIVE rows use literal 'ACTIVE';
  -- terminal rows set active_slot NULL, enforcing one live grant per scope.
  UNIQUE KEY uq_portal_report_grant_active
    (tenant_id, customer_id, request_id, account_id, active_slot),
  KEY idx_portal_report_grant_expiry
    (tenant_id, customer_id, account_id, status, expires_at),
  CONSTRAINT fk_portal_report_grant_request
    FOREIGN KEY (tenant_id, customer_id, request_id)
    REFERENCES portal_report_requests (tenant_id, customer_id, id),
  CONSTRAINT chk_portal_report_grant_status
    CHECK (status IN ('ACTIVE','EXPIRED','FROZEN','REVOKED')),
  CONSTRAINT chk_portal_report_grant_active_slot
    CHECK ((status = 'ACTIVE' AND active_slot = 'ACTIVE') OR
           (status <> 'ACTIVE' AND active_slot IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_report_download_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  grant_id BIGINT UNSIGNED NULL,
  account_id VARCHAR(128) NOT NULL DEFAULT '',
  event_type VARCHAR(64) NOT NULL,
  result VARCHAR(32) NOT NULL,
  reason_code VARCHAR(64) NOT NULL DEFAULT '',
  ip_hash VARCHAR(64) NOT NULL DEFAULT '',
  device_hash VARCHAR(64) NOT NULL DEFAULT '',
  request_trace VARCHAR(128) NOT NULL DEFAULT '',
  idempotency_hash VARCHAR(64) NOT NULL DEFAULT '',
  -- Nullable for normal events. Invalid-token denials set one opaque hourly
  -- account/report bucket so arbitrary token guesses cannot grow audit rows
  -- without bound; the token and its digest are never part of this value.
  dedupe_key VARCHAR(64) NULL,
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_download_dedupe (dedupe_key),
  KEY idx_portal_report_download_timeline
    (tenant_id, customer_id, request_id, occurred_at, id),
  KEY idx_portal_report_download_grant (grant_id, occurred_at),
  CONSTRAINT fk_portal_report_download_request
    FOREIGN KEY (tenant_id, customer_id, request_id)
    REFERENCES portal_report_requests (tenant_id, customer_id, id),
  CONSTRAINT fk_portal_report_download_grant
    FOREIGN KEY (grant_id) REFERENCES portal_report_grants (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Application DB users must receive INSERT/SELECT only on
-- portal_report_download_events; UPDATE/DELETE are intentionally unsupported.
