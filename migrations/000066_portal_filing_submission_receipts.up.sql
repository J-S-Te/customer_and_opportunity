-- customer_portal schema
-- A local immutable snapshot is not proof of submission to public security.
-- Only the provider delivery worker may create this receipt and then move the
-- filing from WAITING_CONTRACT to SUBMITTED in the same transaction.
CREATE TABLE portal_filing_submission_receipts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  filing_id BIGINT UNSIGNED NOT NULL,
  submission_id BIGINT UNSIGNED NOT NULL,
  event_id VARCHAR(64) NOT NULL,
  provider_receipt_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  provider_authority VARCHAR(128) NOT NULL,
  provider_evidence_cipher MEDIUMBLOB NOT NULL,
  provider_evidence_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  received_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_filing_receipt_submission (tenant_id,submission_id),
  UNIQUE KEY uq_portal_filing_receipt_external (provider_authority,provider_receipt_id),
  KEY idx_portal_filing_receipt_filing (filing_id),
  CONSTRAINT fk_portal_filing_receipt_filing FOREIGN KEY (filing_id) REFERENCES portal_filings(id),
  CONSTRAINT fk_portal_filing_receipt_submission FOREIGN KEY (submission_id) REFERENCES portal_filing_submission_snapshots(id),
  CONSTRAINT chk_portal_filing_receipt_digest CHECK (provider_evidence_sha256 REGEXP '^[0-9a-f]{64}$')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE portal_filings DROP CHECK chk_portal_filing_status;
ALTER TABLE portal_filings
  ADD CONSTRAINT chk_portal_filing_status CHECK (status IN ('DRAFT','WAITING_CONTRACT','SUBMITTING','SUBMISSION_FAILED','SUBMITTED'));

ALTER TABLE portal_filing_submission_outbox DROP CHECK chk_portal_filing_outbox_status;
ALTER TABLE portal_filing_submission_outbox
  ADD CONSTRAINT chk_portal_filing_outbox_status CHECK (status IN ('WAITING_CONTRACT','PENDING','PROCESSING','SENT','DEAD_LETTER','CANCELED'));

-- Existing WAITING_CONTRACT rows deliberately remain without receipts. No
-- provider identity, receipt number or submission time can be inferred.
