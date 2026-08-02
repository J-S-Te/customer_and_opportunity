-- Empty/test environments only. Dropping this table destroys authoritative
-- external-submission evidence and is not a normal production rollback.
ALTER TABLE portal_filing_submission_outbox DROP CHECK chk_portal_filing_outbox_status;
UPDATE portal_filing_submission_outbox SET status = 'DEAD_LETTER' WHERE status = 'CANCELED';
ALTER TABLE portal_filing_submission_outbox
  ADD CONSTRAINT chk_portal_filing_outbox_status CHECK (status IN ('WAITING_CONTRACT','PENDING','PROCESSING','SENT','DEAD_LETTER'));
ALTER TABLE portal_filings DROP CHECK chk_portal_filing_status;
UPDATE portal_filings SET status = 'WAITING_CONTRACT' WHERE status IN ('SUBMITTING','SUBMISSION_FAILED');
ALTER TABLE portal_filings
  ADD CONSTRAINT chk_portal_filing_status CHECK (status IN ('DRAFT','WAITING_CONTRACT','SUBMITTED'));
DROP TABLE IF EXISTS portal_filing_submission_receipts;
