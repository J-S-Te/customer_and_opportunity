-- Bind an approval callback to the exact authoritative task and approver that CRM resolved
-- when accepting the action. Existing pending approvals remain fail-closed until the actor
-- submits a fresh action and the three fields are populated atomically with its outbox.
ALTER TABLE crm_presale_approval_instances
  ADD COLUMN pending_task_id VARCHAR(128) NOT NULL DEFAULT '' AFTER last_event_seq,
  ADD COLUMN pending_approver VARCHAR(64) NOT NULL DEFAULT '' AFTER pending_task_id,
  ADD COLUMN pending_action VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER pending_approver,
  ADD CONSTRAINT chk_presale_pending_approval_binding CHECK (
    (pending_task_id = '' AND pending_approver = '' AND pending_action = '') OR
    (pending_task_id <> '' AND pending_approver <> '' AND pending_action IN ('PASS','REJECT'))
  );
