-- Controlled rollback only. Removing idempotency metadata re-opens duplicate
-- immutable progress writes; prefer a forward fix in production.
ALTER TABLE crm_presale_progress_logs
  DROP INDEX uq_presale_progress_key,
  DROP COLUMN request_hash,
  DROP COLUMN idempotency_key;
