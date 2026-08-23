-- CRM 站内信投递到基础平台发生不可重试的校验错误时，记录终态，避免永久高频重放。
ALTER TABLE crm_notifications
  ADD COLUMN platform_delivery_failed_at DATETIME(3) NULL AFTER platform_delivered_at,
  ADD COLUMN platform_delivery_error_code VARCHAR(64) NOT NULL DEFAULT '' AFTER platform_delivery_failed_at;
