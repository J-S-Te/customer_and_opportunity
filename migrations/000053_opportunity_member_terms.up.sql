-- Target schema: CRM. Run after 000051_presale_progress_notifications.up.sql.
ALTER TABLE crm_opportunity_members
  ADD UNIQUE KEY uq_opportunity_member_tenant_id (tenant_id,id);

CREATE TABLE crm_opportunity_member_terms (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  member_id BIGINT UNSIGNED NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  role VARCHAR(32) NOT NULL,
  started_at DATETIME(3) NULL,
  snapshot_at DATETIME(3) NULL,
  active_at_snapshot BOOLEAN NULL,
  ended_at DATETIME(3) NULL,
  started_by VARCHAR(64) NULL,
  ended_by VARCHAR(64) NULL,
  source_kind VARCHAR(32) NOT NULL,
  active_user_id VARCHAR(64) GENERATED ALWAYS AS
    (CASE
       WHEN source_kind = 'RECORDED' AND ended_at IS NULL THEN user_id
       WHEN source_kind = 'LEGACY_SNAPSHOT' AND active_at_snapshot = TRUE AND ended_at IS NULL THEN user_id
       ELSE NULL
     END) STORED,
  PRIMARY KEY (id),
  KEY idx_opportunity_member_term_page (tenant_id,opportunity_id,started_at,id),
  KEY idx_opportunity_member_term_subject (tenant_id,opportunity_id,user_id,started_at,id),
  KEY idx_opportunity_member_term_member (tenant_id,member_id),
  UNIQUE KEY uq_opportunity_member_active_term (tenant_id,opportunity_id,active_user_id),
  CONSTRAINT fk_opportunity_member_term_opportunity
    FOREIGN KEY (tenant_id,opportunity_id) REFERENCES crm_opportunities(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT fk_opportunity_member_term_member
    FOREIGN KEY (tenant_id,member_id) REFERENCES crm_opportunity_members(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT chk_opportunity_member_term_role
    CHECK (role IN ('SALES_SUPPORT','TECHNICAL_SUPPORT','BUSINESS_SUPPORT','OTHER')),
  CONSTRAINT chk_opportunity_member_term_source
    CHECK (source_kind IN ('RECORDED','LEGACY_SNAPSHOT')),
  CONSTRAINT chk_opportunity_member_term_source_fields
    CHECK (
      (source_kind = 'RECORDED' AND started_at IS NOT NULL AND started_by IS NOT NULL
        AND snapshot_at IS NULL AND active_at_snapshot IS NULL)
      OR
      (source_kind = 'LEGACY_SNAPSHOT' AND started_at IS NULL AND started_by IS NULL
        AND snapshot_at IS NOT NULL AND active_at_snapshot IS NOT NULL)
    ),
  CONSTRAINT chk_opportunity_member_term_time
    CHECK (ended_at IS NULL OR ended_at >= COALESCE(started_at,snapshot_at)),
  CONSTRAINT chk_opportunity_member_term_ended_by
    CHECK ((ended_at IS NULL AND ended_by IS NULL) OR (ended_at IS NOT NULL AND ended_by IS NOT NULL)),
  CONSTRAINT chk_opportunity_member_legacy_inactive
    CHECK (source_kind <> 'LEGACY_SNAPSHOT' OR active_at_snapshot = TRUE OR (ended_at IS NULL AND ended_by IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- The old canonical table retained only one row per subject, so it cannot
-- reconstruct any historical interval. Preserve only what can be observed at
-- this migration instant. In particular, neither the reusable member row's
-- original created_at nor its current ended_at is a trustworthy boundary for
-- the current term. A legacy row that was active at the snapshot remains the
-- active tracking baseline until a post-migration operation closes it.
INSERT INTO crm_opportunity_member_terms
  (tenant_id,opportunity_id,member_id,user_id,role,started_at,snapshot_at,active_at_snapshot,
   ended_at,started_by,ended_by,source_kind)
SELECT tenant_id,opportunity_id,id,user_id,role,NULL,CURRENT_TIMESTAMP(3),is_active,
       NULL,NULL,NULL,'LEGACY_SNAPSHOT'
FROM crm_opportunity_members
WHERE deleted_at IS NULL;
