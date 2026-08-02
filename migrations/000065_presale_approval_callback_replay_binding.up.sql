-- Persist every identity used to authenticate an approval callback replay.
-- Existing rows predate this evidence and deliberately receive empty/zero
-- sentinels: they can be shown in history but cannot satisfy the strict replay
-- equality check introduced with this migration.
ALTER TABLE crm_presale_approval_logs
  ADD COLUMN engine_instance_id VARCHAR(128) NOT NULL DEFAULT '' AFTER engine_task_id,
  ADD COLUMN event_sequence BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER engine_instance_id;

CREATE INDEX idx_presale_approval_instance_sequence
  ON crm_presale_approval_logs (tenant_id, engine_instance_id, event_sequence);
