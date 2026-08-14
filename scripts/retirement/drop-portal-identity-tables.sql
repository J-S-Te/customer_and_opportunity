-- 客户门户本地身份映射表退役脚本（Phase 5，受控操作，不随迁移自动执行）
--
-- 执行前置条件（全部满足才允许执行）：
--   1. 门户所有环境 PORTAL_USE_PLATFORM_BINDING=true 且已稳定运行一个观察期（≥7 天）；
--   2. CRM 所有环境 PORTAL_MAPPING_PLATFORM_ONLY=true（不再调用门户 provision/disable）；
--   3. 对账确认平台 iam_external_customer_binding 与 CRM crm_portal_identity_links 无残余差异：
--        PLATFORM_BINDING_MISSING / PLATFORM_BINDING_STATUS_MISMATCH 计数为 0；
--   4. crm_portal_compensation_tasks 中 BIND_PLATFORM_CUSTOMER / DISABLE_PLATFORM_CUSTOMER
--      无 PENDING/PROCESSING/RETRY_WAIT 记录；
--   5. 已完成数据保留：执行前导出本库 portal_identity_links、
--      portal_identity_disable_operations 全量备份并离线归档。
--
-- 执行方式（先只读观察，再执行）:
--   1) RENAME TABLE（先只读过渡 7 天，期间门户平台绑定路径不得回退）:
--        RENAME TABLE portal_identity_links TO retired_portal_identity_links;
--        RENAME TABLE portal_identity_disable_operations TO retired_portal_identity_disable_operations;
--   2) 只读观察期结束后执行下方 DROP。
--
-- 目标库：customer_portal（本地容器 basic-platform-local-portal-mysql-1）

-- 观察期校验查询（每次发布前执行）:
-- SELECT COUNT(*) FROM retired_portal_identity_links;
-- SELECT COUNT(*) FROM portal_sessions WHERE created_at > UTC_TIMESTAMP(3) - INTERVAL 7 DAY;

DROP TABLE IF EXISTS retired_portal_identity_links;
DROP TABLE IF EXISTS retired_portal_identity_disable_operations;

-- 门户侧遗留的映射相关代码在下一版本清理；本脚本只负责数据层退役。
