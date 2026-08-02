CREATE TABLE crm_worker_heartbeats (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  worker_type VARCHAR(64) NOT NULL,
  worker_id VARCHAR(128) NOT NULL,
  heartbeat_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_crm_worker_heartbeat (worker_type, worker_id),
  KEY idx_crm_worker_heartbeat_freshness (worker_type, heartbeat_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
