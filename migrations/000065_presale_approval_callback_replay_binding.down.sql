DROP INDEX idx_presale_approval_instance_sequence ON crm_presale_approval_logs;

ALTER TABLE crm_presale_approval_logs
  DROP COLUMN event_sequence,
  DROP COLUMN engine_instance_id;
