-- CRM credit: retain the exact pre/post customer state for every automatic
-- payment evaluation, so a later reversal of the latest settlement can restore
-- the previous level and consecutive counters instead of guessing.
ALTER TABLE crm_customer_credit_payment_records
  ADD COLUMN event_type VARCHAR(64) NOT NULL DEFAULT 'customer.credit.payment.completed.v1' AFTER payment_id,
  ADD COLUMN reversal_of_payment_id VARCHAR(128) NOT NULL DEFAULT '' AFTER event_type,
  ADD COLUMN prev_level VARCHAR(1) NULL AFTER grace_days,
  ADD COLUMN prev_ontime_count INT NULL AFTER prev_level,
  ADD COLUMN prev_late_count INT NULL AFTER prev_ontime_count,
  ADD COLUMN prev_last_eval_at DATETIME(3) NULL AFTER prev_late_count,
  ADD COLUMN result_level VARCHAR(1) NULL AFTER prev_last_eval_at,
  ADD COLUMN result_ontime_count INT NULL AFTER result_level,
  ADD COLUMN result_late_count INT NULL AFTER result_ontime_count,
  ADD COLUMN reversed_at DATETIME(3) NULL AFTER result_late_count,
  ADD COLUMN reversal_event_id VARCHAR(128) NOT NULL DEFAULT '' AFTER reversed_at;
