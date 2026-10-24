-- CRM: explicitly mark platform user recipients. Unmarked historical identities
-- are intentionally not guessed or mass-rewritten into another namespace.
ALTER TABLE crm_presale_assignments
  ADD COLUMN assignee_namespace VARCHAR(16) NOT NULL DEFAULT '' AFTER assignee_id,
  ADD CONSTRAINT chk_presale_assignee_namespace CHECK (assignee_namespace IN ('','USER'));

ALTER TABLE crm_presale_assignment_events
  ADD COLUMN recipient_namespace VARCHAR(16) NOT NULL DEFAULT '' AFTER recipient_person_id,
  ADD CONSTRAINT chk_presale_assignment_event_namespace CHECK (recipient_namespace IN ('','USER'));

ALTER TABLE crm_notifications
  ADD COLUMN recipient_namespace VARCHAR(16) NOT NULL DEFAULT '' AFTER recipient_id,
  ADD CONSTRAINT chk_crm_notification_recipient_namespace CHECK (recipient_namespace IN ('','USER','PERSON'));

ALTER TABLE crm_presale_progress_notification_events
  DROP CHECK chk_presale_progress_notification_shape,
  ADD CONSTRAINT chk_presale_progress_notification_shape CHECK (
    (recipient_namespace='USER' AND recipient_kind='APPLICANT' AND assignment_id=0) OR
    (recipient_namespace IN ('USER','PERSON') AND recipient_kind='CURRENT_ASSIGNEE' AND assignment_id>0)
  );
