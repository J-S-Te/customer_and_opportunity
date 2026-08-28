-- CM-003: keep credit evaluation entirely inside the CRM monolith.  Payment
-- events arrive through the authenticated internal HTTP boundary; no MQ topic
-- or separately deployed credit service is introduced.
ALTER TABLE crm_credit_rule_settings
  ADD COLUMN level_step INT NOT NULL DEFAULT 1 AFTER late_threshold;

ALTER TABLE crm_customer_credit_payment_records
  ADD COLUMN contract_no VARCHAR(64) NOT NULL DEFAULT '' AFTER customer_id,
  ADD COLUMN period_no VARCHAR(64) NOT NULL DEFAULT '' AFTER contract_no,
  ADD COLUMN source_system VARCHAR(64) NOT NULL DEFAULT '' AFTER period_no,
  ADD COLUMN ignore_reason VARCHAR(64) NOT NULL DEFAULT '' AFTER evaluation,
  ADD UNIQUE KEY uq_credit_payment_id (tenant_id,payment_id);

ALTER TABLE crm_customer_credit_logs
  ADD COLUMN application_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER customer_id,
  ADD COLUMN operator_id VARCHAR(64) NOT NULL DEFAULT '' AFTER source,
  ADD KEY idx_credit_log_application (tenant_id,application_id,occurred_at);

ALTER TABLE crm_customer_credit_applications
  ADD COLUMN idempotency_key VARCHAR(128) NOT NULL AFTER applicant_id,
  ADD UNIQUE KEY uq_credit_apply_idempotency (tenant_id,applicant_id,idempotency_key);

-- Credit notifications reuse the existing CRM inbox and platform delivery
-- worker.  A customer reference is needed because neither an opportunity nor a
-- presale request is guaranteed to exist for a credit change.
ALTER TABLE crm_notifications
  ADD COLUMN customer_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER opportunity_id,
  ADD KEY idx_crm_notification_customer (tenant_id,customer_id,created_at),
  DROP CHECK chk_crm_notification_resource_shape,
  ADD CONSTRAINT chk_crm_notification_resource_shape CHECK (
    (type='OPPORTUNITY_OWNER_CHANGED' AND opportunity_id>0 AND customer_id=0 AND request_id=0 AND request_no='' AND assignment_id=0 AND progress_id=0) OR
    (type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED') AND customer_id=0 AND request_id>0 AND request_no<>'' AND assignment_id>0 AND progress_id=0) OR
    (type IN ('PRESALE_PROGRESS_APPLICANT','PRESALE_PROGRESS_ASSIGNEE') AND customer_id=0 AND request_id>0 AND request_no<>'' AND progress_id>0) OR
    (type IN ('PRESALE_DEPARTMENT_SELECTED','PRESALE_COMPLETED','PRESALE_APPROVAL_PENDING','PRESALE_APPROVAL_APPROVED','PRESALE_APPROVAL_REJECTED') AND customer_id=0 AND request_id>0 AND request_no<>'' AND assignment_id=0 AND progress_id=0) OR
    (type IN ('CREDIT_RULE_CHANGED','CREDIT_RULE_CAP_REACHED','CREDIT_APPLICATION_PENDING','CREDIT_APPLICATION_APPROVED','CREDIT_APPLICATION_REJECTED','CREDIT_APPLICATION_INVALIDATED') AND customer_id>0 AND opportunity_id=0 AND request_id=0 AND request_no='' AND assignment_id=0 AND progress_id=0)
  );
