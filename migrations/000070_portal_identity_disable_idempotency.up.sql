-- customer_portal schema only. Machine-request business idempotency is
-- separate from the per-attempt integration nonce replay table.
CREATE TABLE portal_identity_disable_operations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  oauth_client_subject VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  identity_link_id BIGINT UNSIGNED NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL,
  result_version BIGINT UNSIGNED NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_identity_disable_idempotency (tenant_id, oauth_client_subject, idempotency_key),
  KEY idx_portal_identity_disable_link (tenant_id, identity_link_id),
  CONSTRAINT fk_portal_identity_disable_link FOREIGN KEY (identity_link_id) REFERENCES portal_identity_links (id) ON DELETE RESTRICT,
  CONSTRAINT chk_portal_identity_disable_hash CHECK (request_hash REGEXP '^[0-9a-f]{64}$')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing disabled mappings have no original machine idempotency key or
-- request payload and are deliberately not backfilled. The disable transaction
-- also writes one minimized PORTAL_ACCESS_DISABLED row to portal_auth_events;
-- failure of that insert rolls back the mapping, session and operation writes.
