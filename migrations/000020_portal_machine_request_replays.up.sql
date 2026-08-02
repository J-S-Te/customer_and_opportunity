-- customer_portal schema only. Deploy before enabling Portal internal machine routes.
CREATE TABLE portal_machine_request_replays (
  replay_hash VARCHAR(64) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (replay_hash),
  KEY idx_portal_machine_replay_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
