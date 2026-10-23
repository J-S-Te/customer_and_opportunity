# 当前 HTTP 契约

`crm.yaml` 与 `portal.yaml` 描述两个生产启动入口实际注册的 HTTP API。服务前缀分别来自 `APP_PATH_PREFIX`（默认 `/customer-opportunity`）与 `PORTAL_PATH_PREFIX`（默认 `/customer-portal`），文档内的 path 是前缀后的相对路径。

路由完整性不是手工声明：`go run ./cmd/openapi-check` 会解析生产 Gin 注册源并与两个 OpenAPI 3.1 文档逐项比较 path/method，同时检查每个 operation 都显式声明 `security`、`operationId` 与 `responses`。新增、删除或改名路由却未同步契约时，测试会失败。

安全方案：

- `crmSession` / `portalSession` 是服务端 OIDC 会话 Cookie，不是 JWT；Cookie 名可配置，生产为 `Secure`、`HttpOnly`，浏览器写请求还受 Same-Origin/CSRF 中间件保护。
- `machineJWT` 是基础平台签发的 application JWT；服务器校验 issuer、audience、tenant、scope、时间戳与 nonce 防重放。各 operation 的 scope 写在 security requirement 中。
- `Idempotency-Key` 只标注真实 handler 消费该头的命令；乐观锁使用请求体内 `version` 或 `expected_version`，当前 handler 不读取 `If-Match`，文档不虚构该头。
- 下载接口成功响应是二进制流，不使用 JSON envelope；健康检查也是独立简单 JSON。其余 JSON 接口统一使用 `code/message/request_id/data/details` envelope。

本文只覆盖本仓库当前入口，不替外部Provider签版字段。静态文件校验、AES-GCM私有存储、审计风控和PDF水印已有本地实现/装配，不把它们全部归为未交付外部Provider；已有外部报告服务的真实协议见[接入说明](../../docs/portal-report-service-integration.md)。

## 2026-10-13操作口径核对

调用者先分清CRM浏览器/api/v1、CRM机器/api/v1/internal、Portal浏览器/api/v1、Portal机器/internal。机器Client不能当浏览器角色使用；每个Handler/Service仍校验资源、scope、版本或幂等条件，OpenAPI覆盖路由不证明外部Provider可用。

新增信用/目录/两级指派及execution-takeover路由须核对当前routes.go；multipart与二进制响应不同于JSON。信用决定未消费幂等键重放，依靠PENDING/version，不凭请求头宣称全接口持久幂等。备案exports已有真实渲染器，报告下载可完整装配风险/水印；缺配置失败关闭，路径存在仍不证明目标环境可用。

操作成功以资源状态/历史、实际文件或外部回执为准。400/422纠正输入，401登录，403核对权限范围，409刷新版本/查询原结果，503核对capability与运行依赖；记录request_id并保持敏感头不入文档。本轮静态源码依据见 [核验记录](../../../.ai/modules/operation-docs-crm-20261013.md)。
