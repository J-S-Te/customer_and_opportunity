-- Persist only a scoped digest of the unpredictable code rendered into a
-- successful production download. Plaintext tracking codes are never stored.
ALTER TABLE portal_report_download_events
  ADD COLUMN tracking_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER device_hash,
  ADD KEY idx_portal_report_tracking_digest (tenant_id,tracking_digest),
  ADD CONSTRAINT chk_portal_report_tracking_digest CHECK (
    tracking_digest = '' OR (event_type = 'DOWNLOAD_SUCCEEDED' AND result = 'SUCCESS' AND tracking_digest REGEXP '^[0-9a-f]{64}$')
  );
