CREATE TABLE crm_opportunity_catalog_items (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(16) NOT NULL,
  code VARCHAR(32) NOT NULL,
  name VARCHAR(64) NOT NULL,
  normalized_name VARCHAR(64) NOT NULL,
  sort_order INT UNSIGNED NOT NULL DEFAULT 0,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_crm_opportunity_catalog_code (tenant_id, code),
  UNIQUE KEY uq_crm_opportunity_catalog_name (tenant_id, kind, normalized_name),
  UNIQUE KEY uq_crm_opportunity_catalog_tenant_id (tenant_id, id),
  UNIQUE KEY uq_crm_opportunity_catalog_tenant_id_kind (tenant_id, id, kind),
  KEY idx_crm_opportunity_catalog_list (tenant_id, kind, enabled, sort_order, id),
  CONSTRAINT chk_crm_opportunity_catalog_kind CHECK (kind IN ('TYPE','SOURCE')),
  CONSTRAINT chk_crm_opportunity_catalog_name CHECK (CHAR_LENGTH(TRIM(name)) BETWEEN 1 AND 64)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_opportunity_catalog_links (
  tenant_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  catalog_item_id BIGINT UNSIGNED NOT NULL,
  kind VARCHAR(16) NOT NULL,
  sort_order INT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id, opportunity_id, kind, catalog_item_id),
  KEY idx_crm_opportunity_catalog_link_item (tenant_id, catalog_item_id, opportunity_id),
  CONSTRAINT fk_crm_opportunity_catalog_link_item
    FOREIGN KEY (tenant_id, catalog_item_id, kind)
    REFERENCES crm_opportunity_catalog_items (tenant_id, id, kind)
    ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT chk_crm_opportunity_catalog_link_kind CHECK (kind IN ('TYPE','SOURCE'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_opportunity_catalog_idempotency (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  actor_id VARCHAR(64) NOT NULL,
  operation VARCHAR(16) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  response_json JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_crm_opportunity_catalog_idem (tenant_id, actor_id, operation, idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TEMPORARY TABLE tmp_crm_opportunity_catalog_defaults (
  kind VARCHAR(16) NOT NULL,
  name VARCHAR(64) NOT NULL,
  sort_order INT UNSIGNED NOT NULL,
  PRIMARY KEY (kind, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO tmp_crm_opportunity_catalog_defaults (kind, name, sort_order) VALUES
  ('TYPE','等保审查',10),('TYPE','密码应用安全性评估',20),('TYPE','软件测试',30),
  ('TYPE','源代码审计',40),('TYPE','渗透测试',50),('TYPE','漏洞扫描',60),
  ('TYPE','APP安全完整性',70),('TYPE','在线测试',80),('TYPE','安全系数',90),
  ('TYPE','网络安全风险评估',100),('TYPE','缺口分析',110),('TYPE','机房检测',120),
  ('TYPE','网络安全检测服务',130),('TYPE','安全培训',140),('TYPE','安全性测试',150),
  ('TYPE','应急响应服务',160),('TYPE','网络安全攻防演内容',170),('TYPE','安全运维',180),
  ('SOURCE','客户主动咨询',10),('SOURCE','老客户复购/续约',20),('SOURCE','老客户转介绍',30),
  ('SOURCE','公开招标',40),('SOURCE','销售开拓',50),('SOURCE','合作伙伴推荐',60),
  ('SOURCE','展会/活动',70),('SOURCE','政府/主管单位指派',80),('SOURCE','线上渠道',90),
  ('SOURCE','内部转介',100);

CREATE TEMPORARY TABLE tmp_crm_opportunity_catalog_tenants (
  tenant_id VARCHAR(64) NOT NULL PRIMARY KEY
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT IGNORE INTO tmp_crm_opportunity_catalog_tenants (tenant_id)
SELECT tenant_id FROM crm_customers
UNION SELECT tenant_id FROM crm_opportunities
UNION SELECT tenant_id FROM crm_oidc_sessions;

INSERT IGNORE INTO crm_opportunity_catalog_items
  (tenant_id, kind, code, name, normalized_name, sort_order, enabled, version,
   created_by, updated_by, created_at, updated_at)
SELECT tenants.tenant_id, defaults.kind,
       CONCAT(IF(defaults.kind='TYPE','OT-','OS-'), LEFT(SHA2(CONCAT(tenants.tenant_id, ':', defaults.kind, ':', defaults.name), 256), 24)),
       defaults.name, LOWER(REGEXP_REPLACE(TRIM(defaults.name), '[[:space:]]+', '')), defaults.sort_order, TRUE, 1,
       'system:migration', 'system:migration', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3)
FROM tmp_crm_opportunity_catalog_tenants tenants
CROSS JOIN tmp_crm_opportunity_catalog_defaults defaults;

CREATE TEMPORARY TABLE tmp_crm_opportunity_catalog_values (
  tenant_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  kind VARCHAR(16) NOT NULL,
  name VARCHAR(64) NOT NULL,
  normalized_name VARCHAR(64) NOT NULL,
  position INT UNSIGNED NOT NULL,
  KEY idx_tmp_crm_catalog_value (tenant_id, kind, normalized_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO tmp_crm_opportunity_catalog_values
  (tenant_id, opportunity_id, kind, name, normalized_name, position)
WITH RECURSIVE catalog_split AS (
  SELECT tenant_id, id AS opportunity_id, 'TYPE' AS kind,
         TRIM(SUBSTRING_INDEX(type, '、', 1)) AS name,
         CASE WHEN INSTR(type, '、')=0 THEN '' ELSE SUBSTRING(type, INSTR(type, '、') + CHAR_LENGTH('、')) END AS remaining,
         1 AS position
  FROM crm_opportunities WHERE TRIM(type)<>''
  UNION ALL
  SELECT tenant_id, opportunity_id, kind,
         TRIM(SUBSTRING_INDEX(remaining, '、', 1)),
         CASE WHEN INSTR(remaining, '、')=0 THEN '' ELSE SUBSTRING(remaining, INSTR(remaining, '、') + CHAR_LENGTH('、')) END,
         position + 1
  FROM catalog_split WHERE remaining<>'' AND position<50
), source_split AS (
  SELECT tenant_id, id AS opportunity_id, 'SOURCE' AS kind,
         TRIM(SUBSTRING_INDEX(source, '、', 1)) AS name,
         CASE WHEN INSTR(source, '、')=0 THEN '' ELSE SUBSTRING(source, INSTR(source, '、') + CHAR_LENGTH('、')) END AS remaining,
         1 AS position
  FROM crm_opportunities WHERE TRIM(source)<>''
  UNION ALL
  SELECT tenant_id, opportunity_id, kind,
         TRIM(SUBSTRING_INDEX(remaining, '、', 1)),
         CASE WHEN INSTR(remaining, '、')=0 THEN '' ELSE SUBSTRING(remaining, INSTR(remaining, '、') + CHAR_LENGTH('、')) END,
         position + 1
  FROM source_split WHERE remaining<>'' AND position<50
)
SELECT tenant_id, opportunity_id, kind, LEFT(name, 64), LOWER(REGEXP_REPLACE(TRIM(LEFT(name, 64)), '[[:space:]]+', '')), position
FROM catalog_split WHERE name<>''
UNION ALL
SELECT tenant_id, opportunity_id, kind, LEFT(name, 64), LOWER(REGEXP_REPLACE(TRIM(LEFT(name, 64)), '[[:space:]]+', '')), position
FROM source_split WHERE name<>'';

INSERT IGNORE INTO crm_opportunity_catalog_items
  (tenant_id, kind, code, name, normalized_name, sort_order, enabled, version,
   created_by, updated_by, created_at, updated_at)
SELECT values_table.tenant_id, values_table.kind,
       CONCAT(IF(values_table.kind='TYPE','OT-','OS-'), LEFT(SHA2(CONCAT(values_table.tenant_id, ':', values_table.kind, ':', values_table.normalized_name), 256), 24)),
       MIN(values_table.name), values_table.normalized_name, 1000 + MIN(values_table.position), TRUE, 1,
       'system:migration', 'system:migration', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3)
FROM tmp_crm_opportunity_catalog_values values_table
GROUP BY values_table.tenant_id, values_table.kind, values_table.normalized_name;

INSERT IGNORE INTO crm_opportunity_catalog_links
  (tenant_id, opportunity_id, catalog_item_id, kind, sort_order, created_at)
SELECT values_table.tenant_id, values_table.opportunity_id, catalog.id, values_table.kind,
       MIN(values_table.position), UTC_TIMESTAMP(3)
FROM tmp_crm_opportunity_catalog_values values_table
JOIN crm_opportunity_catalog_items catalog
  ON catalog.tenant_id=values_table.tenant_id
 AND catalog.kind=values_table.kind
 AND catalog.normalized_name=values_table.normalized_name
GROUP BY values_table.tenant_id, values_table.opportunity_id, catalog.id, values_table.kind;

DROP TEMPORARY TABLE tmp_crm_opportunity_catalog_values;
DROP TEMPORARY TABLE tmp_crm_opportunity_catalog_tenants;
DROP TEMPORARY TABLE tmp_crm_opportunity_catalog_defaults;
