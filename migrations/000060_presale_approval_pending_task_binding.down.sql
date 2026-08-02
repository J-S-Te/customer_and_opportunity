-- Destructive rollback removes the callback/task binding evidence. Stop approval writes first.
ALTER TABLE crm_presale_approval_instances
  DROP CHECK chk_presale_pending_approval_binding,
  DROP COLUMN pending_action,
  DROP COLUMN pending_approver,
  DROP COLUMN pending_task_id;
