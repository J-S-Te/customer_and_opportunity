-- customer_portal schema only. Apply after 000056_portal_filing_materials_and_submission_outbox.
-- Existing rows cannot be assigned an immutable object version, scan receipt or
-- encryption algorithm safely. They remain unreadable until a controlled
-- re-ingestion writes real evidence; the download service fails closed.
ALTER TABLE portal_report_files
  ADD COLUMN object_version VARCHAR(256) NOT NULL DEFAULT '' AFTER object_key_cipher,
  ADD COLUMN encryption_algorithm VARCHAR(32) NOT NULL DEFAULT '' AFTER encryption_key_ref,
  ADD COLUMN scan_reference VARCHAR(128) NOT NULL DEFAULT '' AFTER encryption_algorithm,
  ADD COLUMN scanned_at DATETIME(3) NULL AFTER scan_reference;

ALTER TABLE portal_report_files
  ADD CONSTRAINT chk_portal_report_file_encryption CHECK (encryption_algorithm IN ('','AES-256-GCM')),
  ADD CONSTRAINT chk_portal_report_file_evidence CHECK (
    (object_version='' AND encryption_algorithm='' AND scan_reference='' AND scanned_at IS NULL)
    OR
    (object_version<>'' AND encryption_algorithm='AES-256-GCM' AND scan_reference<>'' AND scanned_at IS NOT NULL)
  );
