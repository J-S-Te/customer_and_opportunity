# 后端、数据库与 API 统一规范

## 1. Gin 路由

业务 API 统一为 `<path_prefix>/api/v1`。中间件顺序：

```text
Recovery → RequestID → AccessLog → SecureHeaders → Session/Auth
→ TenantContext → Permission → DataScope → CSRF/Idempotency → Handler
```

健康检查 `/healthz` 不经过登录，但只返回依赖状态，不暴露配置。

## 2. 响应格式

```json
{
  "code": "OK",
  "message": "success",
  "request_id": "01J...",
  "data": {}
}
```

分页数据：

```json
{
  "items": [],
  "page": 1,
  "page_size": 20,
  "total": 0
}
```

HTTP 状态：参数错误 400，未登录 401，无权限/越权 403，不存在 404，状态冲突或版本冲突 409，校验失败 422，限流 429，内部错误 500，依赖不可用 503。

## 3. 错误码

格式为 `<DOMAIN>_<RESOURCE>_<REASON>`，例如：

- `CRM_CUSTOMER_DUPLICATE_NAME`
- `CRM_OPPORTUNITY_STAGE_CONFLICT`
- `CRM_PRESALE_INVALID_TRANSITION`
- `PORTAL_ACCOUNT_LOCKED`
- `PORTAL_REPORT_LINK_EXPIRED`
- `COMMON_VERSION_CONFLICT`
- `INTEGRATION_DEPENDENCY_UNAVAILABLE`

前端根据 `code` 做可预期提示，不能解析后端中文 message 判断逻辑。

## 4. 数据库约束

- 所有唯一性必须由数据库唯一索引兜底。
- `tenant_id` 必须进入业务唯一索引，例如 `(tenant_id, customer_no)`。
- 外部业务 ID 使用 `VARCHAR(64)`，枚举使用 `VARCHAR(32)`，不使用 MySQL ENUM，便于迁移。
- JSON 只用于低频扩展字段或快照；可筛选、关联、约束的字段必须拆列。
- 敏感字段采用应用层信封加密（AES-256-GCM + KMS/环境密钥），查询用单独 HMAC 索引，不使用可逆固定 IV。
- 审计表仅 INSERT，应用账号不授予 UPDATE/DELETE。

## 5. GORM 约定

- 关闭自动迁移用于生产，结构变更只走版本化 SQL migration。
- Repository 查询必须显式加 `tenant_id` 和 `deleted_at IS NULL`。
- 事务由 Service 开启，Repository 接受事务句柄/UnitOfWork。
- 使用 `Select` 明确更新字段，避免零值漏更或全字段覆盖。
- 列表查询 DTO 白名单映射排序字段，禁止把用户输入直接拼入 `ORDER BY`。
- 乐观锁更新条件：`WHERE id=? AND tenant_id=? AND version=?`，成功后 `version=version+1`。

## 6. 写接口通用规则

- 创建、跨系统触发、回调、提交、审批、工时登记等接口接受 `Idempotency-Key`。
- 幂等记录保存请求摘要、响应摘要、状态和过期时间；相同键不同请求体返回 409。
- 每次状态改变记录原状态、目标状态、原因、操作者、发生时间和 request_id。
- 外部回调同时校验机器身份、audience/scope、时间戳、防重放和业务幂等键。

## 7. 权限与数据范围

权限编码采用 `<resource>.<action>`：

```text
customer.read/create/update/import/merge/void/export/sensitive.read
opportunity.read/create/update/void/stage.change/export
presale.read/create/approve/assign/progress/worklog/report
portal_account.provision/revoke
```

授权中间件只判断“能否做”；Service/Repository 再判断“能对哪些数据做”。常用范围：`SELF`、`ORG`、`CUSTOMER`、`ALL`。资源 ID 查询必须先套数据范围，防止 IDOR。

## 8. 异步与调度

- outbox 表字段：`event_id/event_type/aggregate_type/aggregate_id/payload/status/retry_count/next_retry_at/created_at/sent_at`。
- Worker 使用 `SELECT ... FOR UPDATE SKIP LOCKED` 抢占任务。
- 指数退避带抖动，达到上限进入 dead-letter，保留人工重试入口。
- 定时任务使用数据库租约或分布式锁，确保多实例只执行一次。
- 外部消费至少一次；本系统必须幂等，不能依赖“消息只来一次”。

## 9. 测试要求

- Service 业务规则单元测试；Repository 使用真实 MySQL Testcontainer 集成测试。
- Handler 覆盖参数绑定、状态码、权限和错误映射。
- 状态机采用表驱动测试，覆盖全部合法和非法迁移。
- 外部系统使用契约 Stub；生产字段变更必须更新契约测试。
- 每个 P0 接口至少覆盖成功、校验失败、越权、幂等重放、并发冲突和依赖失败。

