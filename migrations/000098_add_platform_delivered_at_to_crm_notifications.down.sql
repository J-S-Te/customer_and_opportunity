-- Target schema: CRM. 仅用于受控开发回退；生产环境优先使用前向修复。
ALTER TABLE crm_notifications DROP COLUMN platform_delivered_at;
