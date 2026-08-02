CREATE TABLE portal_worker_heartbeats (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  worker_type VARCHAR(64) NOT NULL,
  instance_id VARCHAR(128) NOT NULL,
  started_at DATETIME(3) NOT NULL,
  last_seen_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_worker_heartbeat (worker_type, instance_id),
  KEY idx_portal_worker_heartbeat_freshness (worker_type, last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
