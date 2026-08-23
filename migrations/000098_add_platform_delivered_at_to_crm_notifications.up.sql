-- CRM 站内信双写平台：记录该通知是否已上送到基础平台中央站内信。
ALTER TABLE crm_notifications ADD COLUMN platform_delivered_at DATETIME(3) NULL AFTER read_at;
