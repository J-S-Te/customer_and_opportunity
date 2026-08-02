-- CM-002 combination-query indexes. Deploy with an online-schema-change tool
-- for large production tables; these statements can acquire metadata locks.
ALTER TABLE crm_customers
  ADD KEY idx_customer_created (tenant_id, created_at, id);

ALTER TABLE crm_customer_followups
  ADD KEY idx_customer_followup_due (tenant_id, next_follow_at, customer_id, followed_at, id);
