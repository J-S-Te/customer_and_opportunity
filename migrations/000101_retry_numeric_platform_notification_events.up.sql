-- Requeue only the historical platform validation failures whose source event
-- identifier starts with a digit. The delivery boundary now maps these stable
-- source identifiers to a platform-compatible CRM_ event code.
UPDATE crm_notifications
SET platform_delivery_failed_at = NULL,
    platform_delivery_error_code = '',
    updated_at = UTC_TIMESTAMP(3)
WHERE platform_delivered_at IS NULL
  AND platform_delivery_error_code = 'PLATFORM_VALIDATION_ERROR'
  AND source_event_id REGEXP '^[0-9]';
