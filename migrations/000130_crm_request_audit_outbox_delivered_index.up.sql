-- Target schema: CRM.
-- 已投递审计行的清理索引（启用 60 天审计清理前的索引门禁）。
--
-- internal/shared/requestaudit.Store.PurgeDelivered 的清理谓词是
--   delivery_status = 'DELIVERED' AND delivered_at IS NOT NULL AND delivered_at < cutoff
--   LIMIT 1000（GORM clause.Limit，无 ORDER BY）
-- 000075 只建了领取索引 idx_request_audit_delivery (delivery_status, next_attempt_at, id)：
-- 前导列等值命中后 delivered_at 既不在索引列里、也不在范围条件里，清理无法定位过期行。
-- 实测（MySQL 8.4，10.8 万已投递行）EXPLAIN DELETE 命中 idx_request_audit_delivery，
-- type=range、rows≈59161、Extra="Using where"：每轮清理（每小时、最多 20 批）都要扫过
-- delivery_status='DELIVERED' 的全部历史行并逐行回表判断 delivered_at，才删得掉本批
-- 1000 行。保留期越长、已投递行越多，单轮成本越高，不是一次性代价。
--
-- 本迁移补 (delivery_status, delivered_at)：前导列等值 + 第二列范围与清理谓词同序，
-- 清理可直接定位 delivered_at < cutoff 的索引区间并截断 LIMIT 批次，不必扫全部已投递行。
-- 只新增二级索引，不改列、不回填、不删除也不改写任何既有行。清理默认关闭
-- （AUDIT_OUTBOX_DELIVERED_RETENTION_DAYS 缺省 0），本索引在显式启用前只占空间，
-- 不改变任何读写语义；启用顺序为「先执行本迁移 → 再打开保留期配置」。
--
-- 可重入：DDL 隐式提交后账本未记录时迁移会重跑，按 information_schema.statistics 判重入
-- （与 000035/data_analysis、000044/project_management 的同名清理索引迁移同风格）：
-- 索引已存在则执行 SELECT 1，不重复建键、不改任何数据。若同名索引已存在但列序不同，
-- 本迁移不替换未知索引；启用清理前须用 SHOW INDEX 核对定义确为
-- (delivery_status, delivered_at)，不符则先人工收敛。
-- 回退见同名 .down.sql：只删索引，不删审计行；已发布迁移默认前向修复。
SET @crm_request_audit_outbox_delivered_at_index_ddl = IF(
  (SELECT COUNT(*) FROM information_schema.statistics
   WHERE table_schema = DATABASE() AND table_name = 'application_request_audit_outbox'
     AND index_name = 'idx_request_audit_delivered_at') = 0,
  'ALTER TABLE application_request_audit_outbox ADD KEY idx_request_audit_delivered_at (delivery_status, delivered_at)',
  'SELECT 1'
);
PREPARE crm_request_audit_outbox_delivered_at_index_stmt FROM @crm_request_audit_outbox_delivered_at_index_ddl;
EXECUTE crm_request_audit_outbox_delivered_at_index_stmt;
DEALLOCATE PREPARE crm_request_audit_outbox_delivered_at_index_stmt;
