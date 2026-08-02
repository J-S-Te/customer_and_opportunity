-- CRM schema only. Add durable actor-submitted progress idempotency metadata.
-- Existing rows predate the HTTP idempotency contract and remain NULL. MySQL
-- unique indexes allow multiple NULL values, while every new HTTP write is
-- required by the application to persist both values.
ALTER TABLE crm_presale_progress_logs
  ADD COLUMN idempotency_key VARCHAR(128) NULL AFTER progress_pct,
  ADD COLUMN request_hash CHAR(64) NULL AFTER idempotency_key,
  ADD UNIQUE KEY uq_presale_progress_key (tenant_id,idempotency_key);

-- Production rollout on a populated log table requires the release
-- platform's online-DDL procedure and metadata-lock/replica-lag checks. No
-- historical business-data backfill is required.
