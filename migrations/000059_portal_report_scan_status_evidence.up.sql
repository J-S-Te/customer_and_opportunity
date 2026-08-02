-- customer_portal schema only. Apply after 000057_portal_report_file_security_evidence.
-- Existing file rows cannot be declared clean retroactively. The empty default
-- keeps them unreadable until controlled re-ingestion records a real clean
-- result alongside the immutable object version and SHA-256 evidence.
ALTER TABLE portal_report_files
  ADD COLUMN scan_status VARCHAR(16) NOT NULL DEFAULT '' AFTER encryption_algorithm;

ALTER TABLE portal_report_files
  ADD CONSTRAINT chk_portal_report_file_scan_status CHECK (
    scan_status=''
    OR
    (
      scan_status='CLEAN'
      AND object_version<>''
      AND encryption_algorithm='AES-256-GCM'
      AND encryption_key_ref<>''
      AND scan_reference<>''
      AND scanned_at IS NOT NULL
      AND file_hash REGEXP '^[0-9a-f]{64}$'
    )
  );
