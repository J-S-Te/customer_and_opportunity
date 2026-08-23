-- Target schema: CRM. 仅用于受控开发回退。
ALTER TABLE crm_notifications
  DROP COLUMN platform_delivery_error_code,
  DROP COLUMN platform_delivery_failed_at;
