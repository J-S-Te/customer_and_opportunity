-- M-10：Portal 登录/激活上下文只做过期清理，从不按租户过滤。
-- 既有 idx_portal_activation_expiry (tenant_id, expires_at, consumed_at) 的最左列是
-- tenant_id，无租户条件的 `expires_at <= now` 清理无法使用它，只能全表/全索引扫描。
-- 本次新增以 expires_at 打头的索引，支撑 CreateActivation 中的有界机会式清理
-- （DELETE ... WHERE expires_at <= ? ORDER BY expires_at LIMIT 1000）。
-- 该索引只服务过期扫描，不改变 state 唯一约束和一次性消费语义。
ALTER TABLE portal_activation_contexts
  ADD KEY idx_portal_activation_expiry_scan (expires_at, consumed_at);
