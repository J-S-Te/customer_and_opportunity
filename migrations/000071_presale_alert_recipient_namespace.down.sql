-- Empty/test environment rollback only. Production rollback would erase the
-- namespace needed to distinguish OIDC users from PMS persons and can violate
-- the old unique key when the same raw ID exists in both namespaces.
ALTER TABLE crm_presale_alerts
  DROP CHECK chk_presale_alert_recipient_kind,
  DROP INDEX uq_presale_alert_dedupe,
  DROP INDEX idx_presale_alert_recipient,
  ADD UNIQUE KEY uq_presale_alert_dedupe (tenant_id,request_id,alert_type,rule_version,recipient_id),
  ADD KEY idx_presale_alert_recipient (tenant_id,recipient_id,status,created_at),
  MODIFY COLUMN created_by VARCHAR(64) NOT NULL,
  MODIFY COLUMN updated_by VARCHAR(64) NOT NULL,
  MODIFY COLUMN recipient_id VARCHAR(64) NOT NULL,
  DROP COLUMN recipient_kind;
