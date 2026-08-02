-- customer_portal schema only. Dropping immutable encryption/scan evidence is
-- unsafe after production ingestion. Use only on an empty test schema.
ALTER TABLE portal_report_files
  DROP CHECK chk_portal_report_file_evidence,
  DROP CHECK chk_portal_report_file_encryption;

ALTER TABLE portal_report_files
  DROP COLUMN scanned_at,
  DROP COLUMN scan_reference,
  DROP COLUMN encryption_algorithm,
  DROP COLUMN object_version;
