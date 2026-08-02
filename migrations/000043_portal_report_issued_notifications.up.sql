-- customer_portal schema only. Run after 000041. A station message is
-- inserted in the same transaction as trusted ISSUED callback processing.
CREATE TABLE portal_report_notifications (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  kind VARCHAR(32) NOT NULL,
  status VARCHAR(16) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  read_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_notification_scope
    (tenant_id,customer_id,id,request_id,account_id),
  UNIQUE KEY uq_portal_report_notification_kind
    (tenant_id,customer_id,request_id,account_id,kind),
  KEY idx_portal_report_notification_account
    (tenant_id,customer_id,account_id,status,created_at,id),
  CONSTRAINT chk_portal_report_notification_kind
    CHECK (kind IN ('REPORT_ISSUED')),
  CONSTRAINT chk_portal_report_notification_status
    CHECK (status IN ('UNREAD','READ')),
  CONSTRAINT chk_portal_report_notification_read_state
    CHECK ((status='UNREAD' AND read_at IS NULL) OR (status='READ' AND read_at IS NOT NULL)),
  CONSTRAINT fk_portal_report_notification_request
    FOREIGN KEY (tenant_id,customer_id,request_id)
    REFERENCES portal_report_requests (tenant_id,customer_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_report_notification_read_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  notification_id BIGINT UNSIGNED NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  request_trace VARCHAR(128) NOT NULL DEFAULT '',
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_report_notification_first_read (notification_id),
  KEY idx_portal_report_notification_read_audit
    (tenant_id,customer_id,account_id,occurred_at,id),
  CONSTRAINT fk_portal_report_notification_read_notice
    FOREIGN KEY (tenant_id,customer_id,notification_id,request_id,account_id)
    REFERENCES portal_report_notifications
      (tenant_id,customer_id,id,request_id,account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No historical notifications are synthesized: old ISSUED rows do not prove
-- when the customer actually became eligible for this new station message.
