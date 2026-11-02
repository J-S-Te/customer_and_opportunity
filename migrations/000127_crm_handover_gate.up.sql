-- 人员离职交接门闩行（CRM）：把"冻结落库"与"责任写入"对同一 (tenant_id, user_id)
-- 串行化，关闭事务外 check-then-act 的竞态窗口。
-- 纯新增、可重复执行：行由 INSERT ... ON DUPLICATE KEY UPDATE 自愈，无需回填历史数据。

CREATE TABLE IF NOT EXISTS crm_handover_gate (
  tenant_id VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
