ALTER TABLE crm_notifications
  DROP CHECK chk_crm_notification_recipient_kind,
  DROP CHECK chk_crm_notification_resource_shape,
  MODIFY COLUMN source_event_id VARCHAR(64) NOT NULL,
  ADD CONSTRAINT chk_crm_notification_recipient_kind CHECK (
    recipient_kind IN ('PREVIOUS_OWNER','NEW_OWNER','ASSIGNEE_ADDED','ASSIGNEE_REMOVED','PROGRESS_APPLICANT','PROGRESS_ASSIGNEE')
  ),
  ADD CONSTRAINT chk_crm_notification_resource_shape CHECK (
    (type='OPPORTUNITY_OWNER_CHANGED' AND opportunity_id>0 AND request_id=0 AND request_no='' AND assignment_id=0 AND progress_id=0) OR
    (type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED') AND request_id>0 AND request_no<>'' AND assignment_id>0 AND progress_id=0) OR
    (type IN ('PRESALE_PROGRESS_APPLICANT','PRESALE_PROGRESS_ASSIGNEE') AND request_id>0 AND request_no<>'' AND progress_id>0)
  );
