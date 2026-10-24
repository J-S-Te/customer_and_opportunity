-- CRM schema. TS-011 批二 request-scoped workflow notifications:
-- team lead executor-assignment todo, technical director rejection and invalidation notices.
ALTER TABLE crm_notifications
  DROP CHECK chk_crm_notification_resource_shape,
  ADD CONSTRAINT chk_crm_notification_resource_shape CHECK (
    (type='OPPORTUNITY_OWNER_CHANGED' AND opportunity_id>0 AND customer_id=0 AND request_id=0 AND request_no='' AND assignment_id=0 AND progress_id=0) OR
    (type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED') AND customer_id=0 AND request_id>0 AND request_no<>'' AND assignment_id>0 AND progress_id=0) OR
    (type IN ('PRESALE_PROGRESS_APPLICANT','PRESALE_PROGRESS_ASSIGNEE') AND customer_id=0 AND request_id>0 AND request_no<>'' AND progress_id>0) OR
    (type IN ('PRESALE_DEPARTMENT_SELECTED','PRESALE_COMPLETED','PRESALE_APPROVAL_PENDING','PRESALE_APPROVAL_APPROVED','PRESALE_APPROVAL_REJECTED','PRESALE_EXECUTOR_ASSIGNMENT_PENDING','PRESALE_EXECUTION_ASSIGNMENT_REJECTED','PRESALE_TEAM_LEAD_INVALID') AND customer_id=0 AND request_id>0 AND request_no<>'' AND assignment_id=0 AND progress_id=0) OR
    (type IN ('CREDIT_RULE_CHANGED','CREDIT_RULE_CAP_REACHED','CREDIT_APPLICATION_PENDING','CREDIT_APPLICATION_APPROVED','CREDIT_APPLICATION_REJECTED','CREDIT_APPLICATION_INVALIDATED') AND customer_id>0 AND opportunity_id=0 AND request_id=0 AND request_no='' AND assignment_id=0 AND progress_id=0)
  );
