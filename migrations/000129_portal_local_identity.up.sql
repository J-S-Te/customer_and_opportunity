-- Portal本地来源只使用自己的权威表；既有CRM记录在首次启用时拒绝自动转换。
CREATE TABLE IF NOT EXISTS portal_customer_authorities (
 tenant_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 application_code VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 mode VARCHAR(16) COLLATE utf8mb4_bin NOT NULL,
 first_used_at DATETIME(3) NOT NULL,
 actor_id VARCHAR(128) NOT NULL,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 PRIMARY KEY (tenant_id, application_code),
 CONSTRAINT chk_portal_customer_authority_mode CHECK (mode IN ('crm','local'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS portal_local_customers (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 tenant_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 created_by VARCHAR(128) NOT NULL, updated_by VARCHAR(128) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 code VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 name VARCHAR(200) NOT NULL, status VARCHAR(16) NOT NULL,
 namespace VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
 PRIMARY KEY(id), UNIQUE KEY uq_portal_local_customer_code(tenant_id,code),
 CONSTRAINT chk_portal_local_customer_namespace CHECK (namespace='portal-local'),
 CONSTRAINT chk_portal_local_customer_status CHECK (status IN ('ACTIVE','DISABLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS portal_local_contacts (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 tenant_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 created_by VARCHAR(128) NOT NULL, updated_by VARCHAR(128) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL,
 display_name VARCHAR(128) NOT NULL,
 phone_cipher VARBINARY(1024) NOT NULL,
 phone_digest CHAR(64) COLLATE utf8mb4_bin NOT NULL,
 phone_masked VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL,
 PRIMARY KEY(id), UNIQUE KEY uq_portal_local_contact_phone(tenant_id,customer_id,phone_digest),
 KEY idx_portal_local_contact_customer(tenant_id,customer_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS portal_local_invitations (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 tenant_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 created_by VARCHAR(128) NOT NULL, updated_by VARCHAR(128) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 invite_no VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL, contact_id BIGINT UNSIGNED NOT NULL,
 platform_user_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 account_no VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 token_hash CHAR(64) COLLATE utf8mb4_bin NOT NULL,
 token_cipher VARBINARY(1024) NOT NULL,
 status VARCHAR(16) NOT NULL, expires_at DATETIME(3) NOT NULL,
 consumed_at DATETIME(3) NULL, revoked_reason VARCHAR(500) NOT NULL DEFAULT '',
 PRIMARY KEY(id), UNIQUE KEY uq_portal_local_invite_no(invite_no),
 UNIQUE KEY uq_portal_local_invite_token(token_hash),
 KEY idx_portal_local_invite_identity(tenant_id,customer_id,platform_user_id,contact_id,status),
 CONSTRAINT chk_portal_local_invite_status CHECK (status IN ('PENDING','CONSUMED','REVOKED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS portal_local_operations (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 tenant_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 created_by VARCHAR(128) NOT NULL, updated_by VARCHAR(128) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 operation_no VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 idempotency_key VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 request_hash CHAR(64) COLLATE utf8mb4_bin NOT NULL,
 kind VARCHAR(32) NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL, contact_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 platform_user_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
 account_no VARCHAR(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
 stage VARCHAR(32) NOT NULL, status VARCHAR(16) NOT NULL,
 last_error VARCHAR(500) NOT NULL DEFAULT '',
 snapshot_cipher VARBINARY(4096) NOT NULL,
 result_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 lease_owner VARCHAR(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
 lease_until DATETIME(3) NULL,
 PRIMARY KEY(id), UNIQUE KEY uq_portal_local_operation_no(operation_no),
 UNIQUE KEY uq_portal_local_operation_idempotency(tenant_id,created_by,idempotency_key),
 KEY idx_portal_local_operation_customer(tenant_id,customer_id,id),
 CONSTRAINT chk_portal_local_operation_kind CHECK (kind IN ('INVITE','DISABLE','REPORT')),
 CONSTRAINT chk_portal_local_operation_status CHECK (status IN ('PROCESSING','RETRY_WAIT','COMPLETED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS portal_local_projects (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 tenant_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 created_by VARCHAR(128) NOT NULL, updated_by VARCHAR(128) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 project_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL,
 name VARCHAR(200) NOT NULL, status VARCHAR(32) NOT NULL,
 progress_pct TINYINT UNSIGNED NOT NULL,
 stage VARCHAR(64) NOT NULL,
 namespace VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
 PRIMARY KEY(id), UNIQUE KEY uq_portal_local_project(tenant_id,project_id),
 KEY idx_portal_local_project_customer(tenant_id,customer_id,id),
 CONSTRAINT chk_portal_local_project_namespace CHECK (namespace='portal-local'),
 CONSTRAINT chk_portal_local_project_progress CHECK (progress_pct<=100)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS portal_local_report_sources (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 tenant_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 created_by VARCHAR(128) NOT NULL, updated_by VARCHAR(128) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 operation_no VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 request_id BIGINT UNSIGNED NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL,
 project_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 account_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 namespace VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
 file_name VARCHAR(255) NOT NULL, sha256 CHAR(64) COLLATE utf8mb4_bin NOT NULL,
 size BIGINT NOT NULL,
 descriptor_cipher VARBINARY(2048) NOT NULL,
 descriptor_hash VARCHAR(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
 PRIMARY KEY(id), UNIQUE KEY uq_portal_local_report_operation(operation_no),
 UNIQUE KEY uq_portal_local_report_request(tenant_id,request_id),
 KEY idx_portal_local_report_customer(tenant_id,customer_id,id),
 CONSTRAINT chk_portal_local_report_namespace CHECK (namespace='portal-local'),
 CONSTRAINT chk_portal_local_report_size CHECK (size>0 AND size<=20971520)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
