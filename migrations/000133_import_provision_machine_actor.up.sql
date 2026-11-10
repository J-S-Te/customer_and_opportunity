-- 历史开立使用 machine:<client_id> 作为完整幂等主体及审计主体。
-- 与现有请求审计链路的 128 字符容量对齐；不改主体值、摘要或既有幂等键。
-- 先备份并迁移，再发布应用。回滚应用时保留扩容，不缩列截断已有机器身份。
ALTER TABLE crm_customer_create_idempotency
  MODIFY actor_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL;

ALTER TABLE crm_opportunity_create_idempotency
  MODIFY actor_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL;

ALTER TABLE crm_audit_events
  MODIFY actor_id VARCHAR(128) NOT NULL;
