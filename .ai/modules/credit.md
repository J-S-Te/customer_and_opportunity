# Module: credit

## 核心入口

- 服务：`internal/modules/credit/service.go`
- HTTP：`internal/modules/credit/handler.go`、`routes.go`
- 数据模型：`internal/modules/credit/model.go`
- 迁移：`migrations/000105_customer_credit.up.sql` 至 `000107_credit_approval_history_and_queries.up.sql`

## 约束

- 所有查询和写入必须带租户边界；销售角色的客户范围收窄为本人创建客户。
- 回款事件以租户内 `event_id` 和 `payment_id` 去重，重复请求必须比较业务载荷。
- 规则配置使用已持久化的 `updated_at` 作为乐观锁令牌。
- 审批、等级更新、信用日志、客户变更日志和通知写入在同一事务内执行。
