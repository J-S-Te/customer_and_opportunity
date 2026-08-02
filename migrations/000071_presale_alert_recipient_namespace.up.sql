-- Existing alert recipient IDs predate the USER/PERSON namespace contract.
-- Their source cannot be reconstructed safely, so retain them as unreadable
-- historical evidence instead of guessing that an OIDC sub equals a PMS ID.
ALTER TABLE crm_presale_alerts
  ADD COLUMN recipient_kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'LEGACY_UNKNOWN' AFTER status,
  MODIFY COLUMN created_by VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN updated_by VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN recipient_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  DROP INDEX uq_presale_alert_dedupe,
  DROP INDEX idx_presale_alert_recipient,
  ADD UNIQUE KEY uq_presale_alert_dedupe (tenant_id,request_id,alert_type,rule_version,recipient_kind,recipient_id),
  ADD KEY idx_presale_alert_recipient (tenant_id,recipient_kind,recipient_id,status,created_at),
  ADD CONSTRAINT chk_presale_alert_recipient_kind CHECK (recipient_kind IN ('USER','PERSON','LEGACY_UNKNOWN'));

-- Queued legacy notifications cannot be delivered to an authenticated
-- namespace. Cancel only PENDING rows and their unsent outbox events; retain
-- already projected UNREAD/READ rows as inaccessible historical evidence.
UPDATE crm_outbox_events e
JOIN crm_presale_alerts a
  ON e.tenant_id=a.tenant_id
 AND e.event_type='PRESALE_ALERT_SITE_MESSAGE'
 AND e.aggregate_type='presale_alert'
 AND e.aggregate_id=CAST(a.id AS CHAR)
SET e.status='CANCELLED'
WHERE e.status='PENDING'
  AND a.recipient_kind='LEGACY_UNKNOWN'
  AND a.status='PENDING';

UPDATE crm_presale_alerts
SET status='CANCELLED',
    updated_at=UTC_TIMESTAMP(3),
    updated_by='migration-000071',
    version=version+1
WHERE recipient_kind='LEGACY_UNKNOWN'
  AND status='PENDING';

-- The default existed only to classify pre-migration rows. New application
-- writes must always state their namespace explicitly.
ALTER TABLE crm_presale_alerts
  ALTER COLUMN recipient_kind DROP DEFAULT;
