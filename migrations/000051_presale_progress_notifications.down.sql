-- Stop presale-progress-notification-worker and CRM writes first. This removes
-- append-only recipient evidence and is only suitable for an empty/test schema
-- or after explicit archival; production should prefer a forward fix.
DELETE FROM crm_notifications
  WHERE type IN ('PRESALE_PROGRESS_APPLICANT','PRESALE_PROGRESS_ASSIGNEE');
DELETE FROM crm_outbox_events
  WHERE event_type='PRESALE_PROGRESS_SITE_NOTIFICATION';

ALTER TABLE crm_notifications
  DROP CHECK chk_crm_notification_resource_shape,
  DROP CHECK chk_crm_notification_recipient_kind,
  ADD CONSTRAINT chk_crm_notification_recipient_kind CHECK (
    recipient_kind IN ('PREVIOUS_OWNER','NEW_OWNER','ASSIGNEE_ADDED','ASSIGNEE_REMOVED')
  ),
  ADD CONSTRAINT chk_crm_notification_resource_shape CHECK (
    (type='OPPORTUNITY_OWNER_CHANGED' AND opportunity_id>0 AND request_id=0 AND request_no='' AND assignment_id=0) OR
    (type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED') AND request_id>0 AND request_no<>'' AND assignment_id>0)
  ),
  DROP INDEX idx_crm_notification_progress,
  DROP COLUMN progress_id;

DROP TABLE IF EXISTS crm_presale_progress_notification_events;

ALTER TABLE crm_presale_progress_logs
  DROP INDEX uq_presale_progress_tenant_id;
