CREATE TABLE crm_portal_invites (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, invite_no VARCHAR(32) NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL, contact_id BIGINT UNSIGNED NOT NULL, platform_user_id VARCHAR(64) NOT NULL,
 portal_account_id VARCHAR(64) NOT NULL, token_hash CHAR(64) NOT NULL, status VARCHAR(16) NOT NULL,
 expires_at DATETIME(3) NOT NULL, used_at DATETIME(3) NULL, revoked_at DATETIME(3) NULL, revoked_reason VARCHAR(500) NOT NULL DEFAULT '',
 created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY(id), UNIQUE KEY uk_invite_no(invite_no),
 UNIQUE KEY uk_invite_token(token_hash), KEY idx_invite_customer(tenant_id,customer_id,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE crm_portal_identity_links (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL,
 contact_id BIGINT UNSIGNED NOT NULL, platform_user_id VARCHAR(64) NOT NULL, portal_account_id VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL, provisioned_at DATETIME(3) NOT NULL, last_verified_at DATETIME(3) NULL,
 created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY(id),
 UNIQUE KEY uk_link_platform(tenant_id,platform_user_id), UNIQUE KEY uk_link_contact(tenant_id,customer_id,contact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- Recovery: revoke mappings first through integrations, then restore from backup; never drop live identity links directly.
