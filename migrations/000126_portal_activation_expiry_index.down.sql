-- 回滚：仅删除为过期扫描新增的二级索引，不丢任何 activation 证据。
-- 删除后清理语句会退化为主键/唯一键扫描，属性能回退而非数据回退；
-- 若已长期运行清理逻辑，还需评估残留历史行的再次清理窗口。
ALTER TABLE portal_activation_contexts
  DROP KEY idx_portal_activation_expiry_scan;
