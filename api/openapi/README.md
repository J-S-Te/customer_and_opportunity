# 当前 HTTP 契约

`crm.yaml` 与 `portal.yaml` 描述两个生产启动入口实际注册的 HTTP API。服务前缀分别来自 `APP_PATH_PREFIX`（默认 `/customer-opportunity`）与 `PORTAL_PATH_PREFIX`（默认 `/customer-portal`），文档内的 path 是前缀后的相对路径。

路由完整性不是手工声明：`go run ./cmd/openapi-check` 会解析生产 Gin 注册源并与两个 OpenAPI 3.1 文档逐项比较 path/method，同时检查每个 operation 都显式声明 `security`、`operationId` 与 `responses`。新增、删除或改名路由却未同步契约时，测试会失败。

安全方案：

- `crmSession` / `portalSession` 是服务端 OIDC 会话 Cookie，不是 JWT；Cookie 名可配置，生产为 `Secure`、`HttpOnly`，浏览器写请求还受 Same-Origin/CSRF 中间件保护。
- `machineJWT` 是基础平台签发的 application JWT；服务器校验 issuer、audience、tenant、scope、时间戳与 nonce 防重放。各 operation 的 scope 写在 security requirement 中。
- `Idempotency-Key` 只标注真实 handler 消费该头的命令；乐观锁使用请求体内 `version` 或 `expected_version`，当前 handler 不读取 `If-Match`，文档不虚构该头。
- 下载接口成功响应是二进制流，不使用 JSON envelope；健康检查也是独立简单 JSON。其余 JSON 接口统一使用 `code/message/request_id/data/details` envelope。

对象存储、扫描、加密水印、风险网关、基础平台管理、审批、PMS、报价/投标和公安提交均为外部 Provider；本文档只覆盖本仓库当前入口，不声明这些尚未签版的正式 Provider 字段。
