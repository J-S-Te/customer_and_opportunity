ALTER TABLE crm_notifications
  DROP CHECK chk_crm_notification_resource_shape,
  DROP KEY idx_crm_notification_customer,
  DROP COLUMN customer_id;
ALTER TABLE crm_customer_credit_applications DROP KEY uq_credit_apply_idempotency, DROP COLUMN idempotency_key;
ALTER TABLE crm_customer_credit_logs DROP KEY idx_credit_log_application, DROP COLUMN operator_id, DROP COLUMN application_id;
ALTER TABLE crm_customer_credit_payment_records
  DROP KEY uq_credit_payment_id, DROP COLUMN ignore_reason, DROP COLUMN source_system,
  DROP COLUMN period_no, DROP COLUMN contract_no;
ALTER TABLE crm_credit_rule_settings DROP COLUMN level_step;
