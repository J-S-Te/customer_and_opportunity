-- 人工台账只保存离线资料事实，不回填外部可信快照，不触发阶段和金额核对。
-- 先备份并发布本加法迁移，再部署读取台账的应用；已有数据后仅向前修复。
CREATE TABLE crm_opportunity_commercial_records (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  `type` VARCHAR(16) NOT NULL,
  title VARCHAR(200) NOT NULL,
  document_no VARCHAR(64) NOT NULL DEFAULT '',
  record_date DATE NOT NULL,
  amount DECIMAL(18,2) NULL,
  status VARCHAR(16) NOT NULL,
  notes TEXT NOT NULL,
  source_kind VARCHAR(16) NOT NULL DEFAULT 'MANUAL',
  is_void BOOLEAN NOT NULL DEFAULT FALSE,
  void_reason VARCHAR(500) NOT NULL DEFAULT '',
  created_by VARCHAR(64) NOT NULL,
  created_by_name VARCHAR(200) NOT NULL DEFAULT '',
  updated_by VARCHAR(64) NOT NULL,
  updated_by_name VARCHAR(200) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_commercial_record_public (tenant_id, public_id),
  UNIQUE KEY uq_commercial_record_tenant_id (tenant_id, id),
  KEY idx_commercial_record_opportunity (tenant_id, opportunity_id, created_at, id),
  CONSTRAINT fk_commercial_record_opportunity FOREIGN KEY (tenant_id, opportunity_id)
    REFERENCES crm_opportunities (tenant_id, id),
  CONSTRAINT chk_commercial_record_source CHECK (source_kind = 'MANUAL'),
  CONSTRAINT chk_commercial_record_amount CHECK (amount IS NULL OR amount >= 0),
  CONSTRAINT chk_commercial_record_status CHECK (
    (`type` = 'QUOTATION' AND status IN ('DRAFT','SUBMITTED','APPROVED','REJECTED','CANCELLED'))
    OR (`type` = 'BID' AND status IN ('PREPARING','SUBMITTED','WON','LOST','CANCELLED'))
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_opportunity_commercial_record_attachments (
  tenant_id VARCHAR(64) NOT NULL,
  record_id BIGINT UNSIGNED NOT NULL,
  attachment_id VARCHAR(64) NOT NULL,
  position INT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, record_id, attachment_id),
  UNIQUE KEY uq_commercial_attachment_position (tenant_id, record_id, position),
  CONSTRAINT fk_commercial_attachment_record FOREIGN KEY (tenant_id, record_id)
    REFERENCES crm_opportunity_commercial_records (tenant_id, id),
  CONSTRAINT fk_commercial_attachment_file FOREIGN KEY (tenant_id, attachment_id)
    REFERENCES crm_opportunity_attachments (tenant_id, public_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
