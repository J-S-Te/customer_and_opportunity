ALTER TABLE crm_portal_invites
    ADD COLUMN account_no VARCHAR(64) NOT NULL DEFAULT '' AFTER platform_user_id;

UPDATE crm_portal_invites AS invite
JOIN crm_portal_provision_operations AS operation
  ON operation.tenant_id = invite.tenant_id
 AND operation.invite_id = invite.id
 AND operation.deleted_at IS NULL
SET invite.account_no = operation.account_no
WHERE invite.account_no = '' AND operation.account_no <> '';
