-- Stop presale-assignment-notification-worker and the CRM API before this
-- controlled empty/test-environment rollback. Production should archive the
-- append-only evidence and personal notifications and prefer a forward fix.
-- These deletes are destructive and therefore allowed only under the empty or
-- explicitly archived rollback conditions above; never run this down migration
-- as a routine production rollback.
DELETE FROM crm_notifications
  WHERE type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED');
DELETE FROM crm_outbox_events
  WHERE event_type = 'PRESALE_ASSIGNMENT_SITE_NOTIFICATION';

ALTER TABLE crm_notifications
  DROP CHECK chk_crm_notification_resource_shape,
  DROP CHECK chk_crm_notification_recipient_kind,
  ADD CONSTRAINT chk_crm_notification_recipient_kind CHECK (
    recipient_kind IN ('PREVIOUS_OWNER','NEW_OWNER')
  ),
  DROP INDEX idx_crm_notification_request,
  DROP COLUMN assignment_id,
  DROP COLUMN request_no,
  DROP COLUMN request_id;

DROP TABLE IF EXISTS crm_presale_assignment_events;

ALTER TABLE crm_presale_assignments
  DROP INDEX uq_presale_assignment_tenant_id;
