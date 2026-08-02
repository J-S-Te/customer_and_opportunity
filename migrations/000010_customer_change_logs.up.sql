CREATE TABLE crm_customer_change_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  field_name VARCHAR(128) NOT NULL,
  before_json JSON NULL,
  after_json JSON NULL,
  reason VARCHAR(500) NOT NULL,
  operator_id VARCHAR(64) NOT NULL,
  request_id VARCHAR(64) NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_customer_change_resource (tenant_id, customer_id, occurred_at, id),
  KEY idx_customer_change_request (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Append-only business audit. Production rollback is forward-only and must retain rows.
