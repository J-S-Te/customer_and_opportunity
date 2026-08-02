-- customer_portal schema only. Run after 000066. Existing FROZEN grants do
-- not prove which trusted rule fired or when an operator first saw it, so this
-- migration deliberately does not synthesize historical alerts.
ALTER TABLE portal_report_grants
  ADD UNIQUE KEY uq_portal_report_grant_scope_id
    (tenant_id,customer_id,request_id,id);

CREATE TABLE portal_report_risk_alerts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  public_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  grant_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  risk_code VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  active_slot VARCHAR(8) NULL,
  detected_at DATETIME(3) NOT NULL,
  acknowledged_at DATETIME(3) NULL,
  resolved_at DATETIME(3) NULL,
  resolved_by VARCHAR(128) NOT NULL DEFAULT '',
  resolution_action VARCHAR(32) NOT NULL DEFAULT '',
  resolution_reason VARCHAR(500) NOT NULL DEFAULT '',
  request_trace VARCHAR(128) NOT NULL DEFAULT '',
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_risk_alert_public (tenant_id,public_id),
  UNIQUE KEY uq_portal_report_risk_alert_open (grant_id,active_slot),
  KEY idx_portal_report_risk_customer
    (tenant_id,customer_id,account_id,status,detected_at,id),
  KEY idx_portal_report_risk_operator
    (tenant_id,status,detected_at,id),
  CONSTRAINT fk_portal_report_risk_request
    FOREIGN KEY (tenant_id,customer_id,request_id)
    REFERENCES portal_report_requests (tenant_id,customer_id,id),
  CONSTRAINT fk_portal_report_risk_grant
    FOREIGN KEY (tenant_id,customer_id,request_id,grant_id)
    REFERENCES portal_report_grants (tenant_id,customer_id,request_id,id),
  CONSTRAINT chk_portal_report_risk_status
    CHECK (status IN ('OPEN','RESOLVED')),
  CONSTRAINT chk_portal_report_risk_active_slot
    CHECK ((status='OPEN' AND active_slot='OPEN' AND resolved_at IS NULL AND resolved_by='' AND resolution_action='' AND resolution_reason='') OR
           (status='RESOLVED' AND active_slot IS NULL AND resolved_at IS NOT NULL AND resolved_by<>'' AND resolution_action IN ('UNFREEZE','REVOKE_AND_REISSUE') AND resolution_reason<>'')),
  CONSTRAINT chk_portal_report_risk_ack
    CHECK (acknowledged_at IS NULL OR acknowledged_at>=detected_at),
  CONSTRAINT chk_portal_report_risk_resolved
    CHECK (resolved_at IS NULL OR resolved_at>=detected_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_report_risk_review_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  alert_id BIGINT UNSIGNED NOT NULL,
  actor_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
  action VARCHAR(32) NOT NULL,
  idempotency_hash VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
  payload_hash VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
  request_trace VARCHAR(128) NOT NULL DEFAULT '',
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_risk_review_key
    (tenant_id,actor_id,idempotency_hash),
  KEY idx_portal_report_risk_review_alert (alert_id,occurred_at,id),
  CONSTRAINT fk_portal_report_risk_review_alert
    FOREIGN KEY (alert_id) REFERENCES portal_report_risk_alerts (id),
  CONSTRAINT chk_portal_report_risk_review_action
    CHECK (action IN ('UNFREEZE','REVOKE_AND_REISSUE'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- portal_report_risk_alerts is account-scoped station evidence. It contains no
-- raw IP, device, token, object reference or provider diagnostics. Review
-- events and download events are append-only operational audit records.
