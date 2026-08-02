-- Apply only to the independent customer_portal database. Browser-facing IDs
-- are random opaque values; session hashes and internal numeric IDs stay private.
ALTER TABLE portal_sessions
  ADD COLUMN public_id VARCHAR(64) NULL AFTER tenant_id,
  ADD COLUMN ip_masked VARCHAR(64) NOT NULL DEFAULT '' AFTER user_agent_hash,
  ADD COLUMN location_snapshot VARCHAR(128) NOT NULL DEFAULT '' AFTER ip_masked,
  ADD COLUMN device_snapshot VARCHAR(200) NOT NULL DEFAULT '' AFTER location_snapshot;

UPDATE portal_sessions
SET public_id = LOWER(HEX(RANDOM_BYTES(24)))
WHERE public_id IS NULL OR public_id = '';

ALTER TABLE portal_sessions
  MODIFY COLUMN public_id VARCHAR(64) NOT NULL,
  ADD UNIQUE KEY uq_portal_session_public_id (public_id);

CREATE TABLE portal_security_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  public_id VARCHAR(64) NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  type VARCHAR(64) NOT NULL,
  risk_level VARCHAR(16) NOT NULL,
  ip_hash VARCHAR(64) NOT NULL DEFAULT '',
  ip_masked VARCHAR(64) NOT NULL DEFAULT '',
  location_snapshot VARCHAR(128) NOT NULL DEFAULT '',
  device_snapshot VARCHAR(200) NOT NULL DEFAULT '',
  reason_code VARCHAR(64) NOT NULL DEFAULT '',
  occurred_at DATETIME(3) NOT NULL,
  acknowledged_at DATETIME(3) NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_security_event_public_id (public_id),
  KEY idx_portal_security_event_subject (tenant_id, platform_user_id, occurred_at),
  KEY idx_portal_security_event_customer (tenant_id, customer_id, occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
