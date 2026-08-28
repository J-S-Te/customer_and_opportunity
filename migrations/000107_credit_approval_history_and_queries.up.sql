-- Generic CRM approval storage.  It is deliberately independent of the
-- presale-specific approval tables so future business types can share it.
CREATE TABLE crm_approval_instances (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  biz_type VARCHAR(64) NOT NULL, business_id BIGINT UNSIGNED NOT NULL,
  status VARCHAR(16) NOT NULL, current_task_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_by VARCHAR(64) NOT NULL, decided_by VARCHAR(64) NOT NULL DEFAULT '',
  decided_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY(id), UNIQUE KEY uq_approval_business(tenant_id,biz_type,business_id),
  KEY idx_approval_instance_pending(tenant_id,biz_type,status,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE crm_approval_tasks (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  instance_id BIGINT UNSIGNED NOT NULL, task_code VARCHAR(64) NOT NULL,
  assignee_role VARCHAR(64) NOT NULL, status VARCHAR(16) NOT NULL,
  decided_by VARCHAR(64) NOT NULL DEFAULT '', decided_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY(id), UNIQUE KEY uq_approval_task(tenant_id,instance_id,task_code),
  KEY idx_approval_task_pending(tenant_id,assignee_role,status,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
ALTER TABLE crm_customer_credit_applications ADD COLUMN approval_instance_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER id;
CREATE TABLE crm_credit_rule_setting_versions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  grace_days INT NOT NULL, on_time_threshold INT NOT NULL, late_threshold INT NOT NULL,
  level_step INT NOT NULL, enabled BOOLEAN NOT NULL, changed_by VARCHAR(64) NOT NULL,
  reason VARCHAR(500) NOT NULL DEFAULT '', changed_at DATETIME(3) NOT NULL,
  PRIMARY KEY(id), KEY idx_credit_rule_versions(tenant_id,changed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
