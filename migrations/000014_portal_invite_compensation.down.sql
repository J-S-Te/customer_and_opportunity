DROP TABLE IF EXISTS crm_portal_compensation_tasks;
ALTER TABLE crm_portal_identity_links MODIFY COLUMN platform_user_id VARCHAR(64) NOT NULL;
ALTER TABLE crm_portal_invites MODIFY COLUMN platform_user_id VARCHAR(64) NOT NULL;

-- Recovery warning: run the narrowing ALTER only after proving every stored
-- platform_user_id is at most 64 characters. Otherwise keep the widened
-- columns and forward-fix; never truncate identity subjects.
