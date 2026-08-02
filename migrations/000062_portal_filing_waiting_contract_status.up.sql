-- A customer-locked immutable snapshot is not proof that the filing was
-- submitted to public security. Rename existing local-only SUBMITTED rows to
-- WAITING_CONTRACT until a future signed provider receipt completes the flow.
ALTER TABLE portal_filings DROP CHECK chk_portal_filing_status;

UPDATE portal_filings
SET status = 'WAITING_CONTRACT'
WHERE status = 'SUBMITTED';

ALTER TABLE portal_filings
  ADD CONSTRAINT chk_portal_filing_status CHECK (status IN ('DRAFT','WAITING_CONTRACT','SUBMITTED'));
