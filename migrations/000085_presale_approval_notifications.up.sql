-- CRM schema. Approval workflow notifications are personal request-scoped
-- notifications and must be accepted by the existing notification shape check.
ALTER TABLE crm_notifications
  DROP CHECK chk_crm_notification_resource_shape,
  ADD CONSTRAINT chk_crm_notification_resource_shape CHECK (
    (type='OPPORTUNITY_OWNER_CHANGED' AND opportunity_id>0 AND request_id=0 AND request_no='' AND assignment_id=0 AND progress_id=0) OR
    (type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED') AND request_id>0 AND request_no<>'' AND assignment_id>0 AND progress_id=0) OR
    (type IN ('PRESALE_PROGRESS_APPLICANT','PRESALE_PROGRESS_ASSIGNEE') AND request_id>0 AND request_no<>'' AND progress_id>0) OR
    (type IN ('PRESALE_DEPARTMENT_SELECTED','PRESALE_COMPLETED','PRESALE_APPROVAL_PENDING','PRESALE_APPROVAL_APPROVED','PRESALE_APPROVAL_REJECTED') AND request_id>0 AND request_no<>'' AND assignment_id=0 AND progress_id=0)
  );
