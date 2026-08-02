-- customer_portal schema only. Apply after 000054_portal_project_message_keyset_reads.
-- This migration adds provider-neutral security state for filing materials and
-- an internal submission outbox. It deliberately does not define police-system
-- request fields, URLs, receipt semantics or an official PDF template: those
-- remain blocked until the corresponding signed contracts are versioned.
CREATE TABLE portal_filing_materials (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL,
  filing_id BIGINT UNSIGNED NOT NULL,
  material_code VARCHAR(48) NOT NULL,
  object_key_cipher VARBINARY(1024) NOT NULL,
  object_version VARCHAR(256) NOT NULL DEFAULT '',
  file_name VARCHAR(255) NOT NULL,
  mime_type VARCHAR(160) NOT NULL,
  size_bytes BIGINT UNSIGNED NOT NULL,
  sha256 CHAR(64) NOT NULL,
  scan_status VARCHAR(32) NOT NULL,
  scan_reference VARCHAR(128) NOT NULL DEFAULT '',
  finalize_lease_until DATETIME(3) NULL,
  create_actor_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  create_key_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  create_request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  uploaded_at DATETIME(3) NULL,
  scanned_at DATETIME(3) NULL,
  created_by VARCHAR(128) NOT NULL,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_material_public (tenant_id,public_id),
  UNIQUE KEY uq_portal_filing_material_code (tenant_id,filing_id,material_code),
  UNIQUE KEY uq_portal_filing_material_create (tenant_id,create_actor_id,create_key_hash),
  KEY idx_portal_filing_material_scan (tenant_id,scan_status,updated_at),
  CONSTRAINT fk_portal_filing_material_filing FOREIGN KEY (filing_id) REFERENCES portal_filings(id),
  CONSTRAINT chk_portal_filing_material_code CHECK (material_code IN ('NETWORK_TOPOLOGY','SECURITY_GOVERNANCE','SECURITY_DESIGN','SECURITY_PRODUCTS','SECURITY_SERVICES','AUTHORITY_GUIDANCE','CLASSIFICATION_REPORT')),
  CONSTRAINT chk_portal_filing_material_mime CHECK (mime_type IN ('application/pdf','image/png','image/jpeg')),
  CONSTRAINT chk_portal_filing_material_size CHECK (size_bytes > 0 AND size_bytes <= 20971520),
  CONSTRAINT chk_portal_filing_material_scan CHECK (scan_status IN ('PENDING_UPLOAD','FINALIZING','SCANNING','CLEAN','REJECTED','SCAN_FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_filing_submission_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  filing_id BIGINT UNSIGNED NOT NULL,
  submission_id BIGINT UNSIGNED NOT NULL,
  contract_version VARCHAR(48) NOT NULL,
  payload JSON NOT NULL,
  payload_sha256 CHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL,
  retry_count INT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3) NULL,
  locked_by VARCHAR(128) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  last_error_summary VARCHAR(1000) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  sent_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_submission_event (tenant_id,event_id),
  UNIQUE KEY uq_portal_filing_submission_outbox (tenant_id,submission_id),
  KEY idx_portal_filing_submission_delivery (status,next_retry_at,locked_until,id),
  CONSTRAINT fk_portal_filing_outbox_filing FOREIGN KEY (filing_id) REFERENCES portal_filings(id),
  CONSTRAINT fk_portal_filing_outbox_submission FOREIGN KEY (submission_id) REFERENCES portal_filing_submission_snapshots(id),
  CONSTRAINT chk_portal_filing_outbox_contract CHECK (contract_version IN ('portal.filing.submission-reference.v1')),
  CONSTRAINT chk_portal_filing_outbox_status CHECK (status IN ('WAITING_CONTRACT','PENDING','PROCESSING','SENT','DEAD_LETTER'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No history is synthesized. Existing submission snapshots predate the
-- material trust chain and the internal event contract, so neither CLEAN scan
-- evidence nor an external-delivery intent can be inferred safely.
