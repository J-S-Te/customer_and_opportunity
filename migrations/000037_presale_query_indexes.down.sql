-- CRM schema only. Roll back only after verifying no production query plan
-- depends on this index and during a controlled online-DDL window.
ALTER TABLE crm_presale_requests
  DROP INDEX idx_presale_expected_status;
