-- customer_portal schema only. Apply after 000028_portal_service_evaluations.
-- Migration number 000030 belongs to the separate CRM schema and is not an
-- ordering predecessor in the customer_portal chain.
-- Sensitive form bodies, submission documents and replay responses are AEAD
-- ciphertext. There is intentionally no plaintext JSON payload column.
CREATE TABLE portal_filings (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL,
  filing_no VARCHAR(48) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  project_id VARCHAR(64) NOT NULL DEFAULT '',
  form_version VARCHAR(16) NOT NULL,
  status VARCHAR(24) NOT NULL,
  current_step TINYINT UNSIGNED NOT NULL DEFAULT 1,
  completion_pct TINYINT UNSIGNED NOT NULL DEFAULT 0,
  submitted_at DATETIME(3) NULL,
  locked_at DATETIME(3) NULL,
  unlocked_at DATETIME(3) NULL,
  unlock_reason_cipher MEDIUMBLOB NULL,
  create_idempotency_key VARCHAR(128) NOT NULL,
  create_request_hash CHAR(64) NOT NULL,
  created_by VARCHAR(128) NOT NULL,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_public_id (public_id),
  UNIQUE KEY uq_portal_filing_no (tenant_id,filing_no),
  UNIQUE KEY uq_portal_filing_create (tenant_id,customer_id,account_id,create_idempotency_key),
  KEY idx_portal_filing_customer (tenant_id,customer_id,status,updated_at),
  CONSTRAINT chk_portal_filing_form_version CHECK (form_version IN ('2025.1')),
  CONSTRAINT chk_portal_filing_status CHECK (status IN ('DRAFT','SUBMITTED')),
  CONSTRAINT chk_portal_filing_step CHECK (current_step BETWEEN 1 AND 7),
  CONSTRAINT chk_portal_filing_completion CHECK (completion_pct BETWEEN 0 AND 100)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_filing_sections (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  filing_id BIGINT UNSIGNED NOT NULL,
  section_code VARCHAR(48) NOT NULL,
  schema_version VARCHAR(16) NOT NULL,
  data_cipher MEDIUMBLOB NOT NULL,
  validation_status VARCHAR(16) NOT NULL,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_section (tenant_id,filing_id,section_code),
  KEY idx_portal_filing_section_filing (tenant_id,filing_id),
  CONSTRAINT chk_portal_filing_section_code CHECK (section_code IN ('ORGANIZATION','CLASSIFIED_OBJECT','CLASSIFICATION','NEW_TECHNOLOGY','MATERIALS','DATA_INVENTORY','CLASSIFICATION_REPORT')),
  CONSTRAINT chk_portal_filing_section_schema CHECK (schema_version IN ('2025.1')),
  CONSTRAINT chk_portal_filing_section_validation CHECK (validation_status IN ('VALID','INVALID'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_filing_matrix (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  filing_id BIGINT UNSIGNED NOT NULL,
  matrix_code VARCHAR(48) NOT NULL,
  row_code VARCHAR(48) NOT NULL DEFAULT '',
  column_code VARCHAR(48) NOT NULL DEFAULT '',
  selected BOOLEAN NOT NULL DEFAULT FALSE,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_matrix (tenant_id,filing_id,matrix_code),
  CONSTRAINT chk_portal_filing_matrix_code CHECK (matrix_code IN ('BUSINESS_INFORMATION','SYSTEM_SERVICE')),
  CONSTRAINT chk_portal_filing_matrix_selection CHECK ((selected=FALSE AND row_code='' AND column_code='') OR (selected=TRUE AND row_code IN ('LEGAL_RIGHTS','PUBLIC_INTEREST','NATIONAL_SECURITY') AND column_code IN ('GENERAL_DAMAGE','SERIOUS_DAMAGE','EXTREME_DAMAGE')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_filing_submission_snapshots (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  filing_id BIGINT UNSIGNED NOT NULL,
  sequence BIGINT UNSIGNED NOT NULL,
  form_version VARCHAR(16) NOT NULL,
  canonical_cipher LONGBLOB NOT NULL,
  snapshot_sha256 CHAR(64) NOT NULL,
  submitted_by VARCHAR(128) NOT NULL,
  submitted_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_submission (tenant_id,filing_id,sequence),
  KEY idx_portal_filing_submission_time (tenant_id,filing_id,submitted_at),
  CONSTRAINT chk_portal_filing_submission_version CHECK (form_version IN ('2025.1'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_filing_actions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  filing_id BIGINT UNSIGNED NOT NULL,
  action VARCHAR(48) NOT NULL,
  actor_type VARCHAR(16) NOT NULL,
  actor_id VARCHAR(128) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  request_cipher MEDIUMBLOB NOT NULL,
  request_id VARCHAR(128) NOT NULL DEFAULT '',
  response_cipher MEDIUMBLOB NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_action (tenant_id,filing_id,actor_id,action,idempotency_key),
  KEY idx_portal_filing_action_timeline (tenant_id,filing_id,occurred_at,id),
  CONSTRAINT chk_portal_filing_action_actor CHECK (actor_type IN ('CUSTOMER','MACHINE'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No material/file table and no export table are created. The first slice only
-- records customer-declared material presence/file names inside encrypted
-- section data; trusted upload, malware scanning, object storage and official
-- 2025 PDF templates remain unavailable and their APIs fail closed.
