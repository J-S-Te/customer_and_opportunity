-- Target schema: CRM.
-- 回滚：只删除 000130 新增的清理索引，不删任何审计行、不改列与约束。
-- 删除后 PurgeDelivered 会退化为扫描全部 DELIVERED 行（性能回退，不是数据回退）。
ALTER TABLE application_request_audit_outbox
  DROP KEY idx_request_audit_delivered_at;
