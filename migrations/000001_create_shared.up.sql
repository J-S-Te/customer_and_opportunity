CREATE TABLE crm_biz_sequences (
  tenant_id VARCHAR(64) NOT NULL, business_date CHAR(8) NOT NULL, business_type VARCHAR(32) NOT NULL,
  current_value BIGINT UNSIGNED NOT NULL, PRIMARY KEY (tenant_id,business_date,business_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE crm_audit_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, event_id VARCHAR(64) NOT NULL, tenant_id VARCHAR(64) NOT NULL,
  application_code VARCHAR(64) NOT NULL, module VARCHAR(64) NOT NULL, operation VARCHAR(64) NOT NULL,
  resource_type VARCHAR(64) NOT NULL, resource_id VARCHAR(64) NOT NULL, actor_id VARCHAR(64) NOT NULL,
  actor_name_snapshot VARCHAR(200) NOT NULL DEFAULT '', before_json JSON NULL, after_json JSON NULL,
  reason VARCHAR(500) NOT NULL DEFAULT '', result VARCHAR(32) NOT NULL, request_id VARCHAR(64) NOT NULL,
  occurred_at DATETIME(3) NOT NULL, PRIMARY KEY(id), UNIQUE KEY uk_audit_event(event_id),
  KEY idx_audit_resource(tenant_id,resource_type,resource_id), KEY idx_audit_request(request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- Recovery: these shared tables may only be dropped before business data exists; production rollback is forward-only.
