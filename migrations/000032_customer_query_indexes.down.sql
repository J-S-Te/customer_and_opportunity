-- Safe structural rollback. Query behavior remains correct but may be slower.
ALTER TABLE crm_customer_followups
  DROP KEY idx_customer_followup_due;

ALTER TABLE crm_customers
  DROP KEY idx_customer_created;
