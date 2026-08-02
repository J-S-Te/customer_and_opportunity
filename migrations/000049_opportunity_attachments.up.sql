CREATE TABLE crm_opportunity_attachments (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  object_key VARCHAR(512) NOT NULL,
  object_version VARCHAR(256) NOT NULL DEFAULT '',
  file_name VARCHAR(255) NOT NULL,
  size_bytes BIGINT UNSIGNED NOT NULL,
  mime_type VARCHAR(160) NOT NULL,
  sha256 CHAR(64) NOT NULL,
  scan_status VARCHAR(32) NOT NULL,
  scan_reference VARCHAR(128) NOT NULL DEFAULT '',
  upload_expires_at DATETIME(3) NULL,
  upload_lease_until DATETIME(3) NULL,
  finalize_lease_until DATETIME(3) NULL,
  uploaded_at DATETIME(3) NULL,
  scanned_at DATETIME(3) NULL,
  create_actor_id VARCHAR(64) NOT NULL,
  create_idempotency_key VARCHAR(128) NOT NULL,
  create_request_hash CHAR(64) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_attachment_public (tenant_id, public_id),
  UNIQUE KEY uq_opportunity_attachment_create (tenant_id, create_actor_id, create_idempotency_key),
  KEY idx_opportunity_attachment (tenant_id, opportunity_id, created_at, id),
  KEY idx_opportunity_attachment_scan (tenant_id, scan_status, updated_at),
  CONSTRAINT fk_opportunity_attachment_opportunity
    FOREIGN KEY (opportunity_id) REFERENCES crm_opportunities(id) ON DELETE RESTRICT,
  CONSTRAINT ck_opportunity_attachment_size CHECK (size_bytes > 0 AND size_bytes <= 20971520),
  CONSTRAINT ck_opportunity_attachment_scan CHECK (scan_status IN ('PENDING_UPLOAD','FINALIZING','SCANNING','CLEAN','REJECTED','SCAN_FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Release order: deploy the additive table before the CRM binary. The binary
-- still fails closed until trusted object-store and scanner adapters are wired.
-- No historical rows are synthesized because legacy binary/object provenance
-- and scan results cannot be established safely.
