-- customer_portal schema only. Submitted snapshots and audit actions are
-- permanent compliance records. Use only in an empty environment or after an
-- approved encrypted export and retention review; prefer forward repair.
DROP TABLE IF EXISTS portal_filing_actions;
DROP TABLE IF EXISTS portal_filing_submission_snapshots;
DROP TABLE IF EXISTS portal_filing_matrix;
DROP TABLE IF EXISTS portal_filing_sections;
DROP TABLE IF EXISTS portal_filings;
