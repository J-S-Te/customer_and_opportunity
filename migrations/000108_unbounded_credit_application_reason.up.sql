-- Credit adjustment reasons are required but intentionally have no application-level length limit.
ALTER TABLE crm_customer_credit_applications MODIFY COLUMN reason TEXT NOT NULL;
