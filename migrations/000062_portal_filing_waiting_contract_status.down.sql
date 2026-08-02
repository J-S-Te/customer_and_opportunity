-- A rollback loses the distinction between a local locked snapshot and a
-- provider-confirmed submission. Stop filing writes and retain an audit copy.
ALTER TABLE portal_filings DROP CHECK chk_portal_filing_status;

UPDATE portal_filings
SET status = 'SUBMITTED'
WHERE status = 'WAITING_CONTRACT';

ALTER TABLE portal_filings
  ADD CONSTRAINT chk_portal_filing_status CHECK (status IN ('DRAFT','SUBMITTED'));
