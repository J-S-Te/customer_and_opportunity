ALTER TABLE crm_customers
    ADD COLUMN credit_level CHAR(1) NOT NULL DEFAULT 'B' AFTER status,
    ADD COLUMN credit_updated_at DATETIME(3) NULL AFTER credit_level,
    ADD COLUMN credit_change_source VARCHAR(16) NOT NULL DEFAULT 'INITIAL' AFTER credit_updated_at,
    ADD COLUMN consecutive_ontime_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER credit_change_source,
    ADD COLUMN consecutive_late_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER consecutive_ontime_count,
    ADD COLUMN last_payment_eval_at DATETIME(3) NULL AFTER consecutive_late_count,
    ADD KEY idx_customer_credit_level (tenant_id, credit_level, updated_at);

CREATE TABLE crm_credit_rule_settings (
    tenant_id VARCHAR(64) NOT NULL, grace_days INT NOT NULL DEFAULT 7,
    on_time_threshold INT NOT NULL DEFAULT 2, late_threshold INT NOT NULL DEFAULT 2,
    enabled BOOLEAN NOT NULL DEFAULT TRUE, updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE crm_customer_credit_payment_records (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
    event_id VARCHAR(128) NOT NULL, payment_id VARCHAR(128) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL,
    due_date DATETIME(3) NOT NULL, paid_date DATETIME(3) NULL, due_amount VARCHAR(32) NOT NULL,
    paid_amount VARCHAR(32) NOT NULL, grace_days INT NOT NULL, evaluation VARCHAR(24) NOT NULL,
    evaluated_at DATETIME(3) NOT NULL, created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id), UNIQUE KEY uq_credit_payment (tenant_id, event_id),
    KEY idx_credit_payment_customer (tenant_id, customer_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE crm_customer_credit_logs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
    customer_id BIGINT UNSIGNED NOT NULL, payment_id VARCHAR(128) NOT NULL DEFAULT '', event_id VARCHAR(128) NOT NULL DEFAULT '',
    from_level CHAR(1) NOT NULL, to_level CHAR(1) NOT NULL, source VARCHAR(16) NOT NULL,
    reason VARCHAR(500) NOT NULL, occurred_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id), KEY idx_credit_log_customer (tenant_id, customer_id, occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE crm_customer_credit_applications (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
    customer_id BIGINT UNSIGNED NOT NULL, applicant_id VARCHAR(64) NOT NULL,
    from_level CHAR(1) NOT NULL, target_level CHAR(1) NOT NULL,
    reason VARCHAR(500) NOT NULL, status VARCHAR(16) NOT NULL,
    opinion VARCHAR(500) NOT NULL DEFAULT '', decided_by VARCHAR(64) NOT NULL DEFAULT '',
    decided_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
    version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    pending_customer_id BIGINT UNSIGNED NULL,
    PRIMARY KEY (id), UNIQUE KEY uq_credit_apply_pending (tenant_id,pending_customer_id),
    KEY idx_credit_apply_pending (tenant_id,status,created_at),
    KEY idx_credit_apply_customer (tenant_id,customer_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
