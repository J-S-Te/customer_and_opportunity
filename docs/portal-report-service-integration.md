# 客户门户与已有外部报告服务接入

用户已确认上游为已有外部报告服务。本模块的真实 HTTP 适配器可调用该服务；历史环境变量 `PORTAL_REPORT_PROJECT_*` 保留兼容，不要求仓内 Project 系统实现报告申请路由。外部服务必须匹配下面的申请、回调和受控文件发布协议。如果现有接口不同，应在其服务侧提供协议适配，不能把不兼容地址填入后宣布完成接入。本次没有访问用户外部服务，正式 URL、机器客户端、协议兼容和真实端到端回执仍需部署联调。

## Worker 两种模式

| 模式 | 设置 | 实际行为 | 就绪能力 |
|---|---|---|---|
| 仅摄取 | `PORTAL_REPORT_WORKER_INGEST_ONLY=true`，默认 | 处理已接受的合法 ISSUED 回调摄取任务，不投递新申请 Outbox | 仅 `portal_report_ingest` 心跳；不会使新建报告申请能力变为 READY |
| 完整投递及摄取 | `PORTAL_REPORT_WORKER_INGEST_ONLY=false` | 将真实申请 Outbox POST 到配置的外部 endpoint，取得下游请求 ID 后推进本地审批状态，再摄取回调文件 | 发布 `portal_report_delivery` 心跳；网络或业务回执失败会走重试/死信，不伪造成功 |

两种模式都要求 `PORTAL_REPORT_WORKER_MYSQL_DSN`（Portal 独立 schema）、`PORTAL_OIDC_TENANT_ID`、`PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64`（与 API 相同的 32 字节描述符密钥）、`PORTAL_REPORT_CONTENT_KEY_BASE64`（另一独立 32 字节内容密钥）、`PORTAL_REPORT_STORE_ROOT`（API/Worker 同一个私有绝对路径卷）、`PORTAL_REPORT_SOURCE_BASE_URL`、`PORTAL_REPORT_SOURCE_TOKEN_URL`、`PORTAL_REPORT_SOURCE_CLIENT_ID`、`PORTAL_REPORT_SOURCE_CLIENT_SECRET`。来源 token scope 固定为 `platform:file:portal_report`。源网关启动配置不允许缺字段或非 HTTP(S) URL。

完整模式另外要求下列字段完整，缺失或无效即启动失败，不注册投递心跳：

- `PORTAL_REPORT_PROJECT_REQUEST_URL`：外部报告服务的完整申请接收 URL。
- `PORTAL_REPORT_PROJECT_TOKEN_URL`：OAuth client_credentials token endpoint。
- `PORTAL_REPORT_PROJECT_CLIENT_ID`、`PORTAL_REPORT_PROJECT_CLIENT_SECRET`：专用申请客户端，不复用来源读取凭据。
- `PORTAL_REPORT_PROJECT_SCOPE=report.request.write`：仅这个 scope。
- 私有 CA/mTLS 按需设置 `PORTAL_REPORT_PROJECT_TLS_ROOT_CA_FILE`、`PORTAL_REPORT_PROJECT_TLS_CLIENT_CERT_FILE`、`PORTAL_REPORT_PROJECT_TLS_CLIENT_KEY_FILE`、`PORTAL_REPORT_PROJECT_TLS_SERVER_NAME`、`PORTAL_REPORT_PROJECT_TLS_REQUIRE_MTLS=true`。证书与私钥必须成对；配置 TLS 材料时 token 与业务 URL 都必须 HTTPS。不可读证书阻止实例启动。

仅摄取模式不创建申请客户端，不读取其证书，不使用 `PROJECT_*` 字段。显式模式优先，填了申请 URL 但仍为 true 不会自动开启投递。Worker 心跳证明进程与本地依赖存活，不替代远端健康检查；远端是否成功仅由实际请求回执证明。

## 申请协议

适配器先向 TokenURL 发送 OAuth `grant_type=client_credentials`、`scope=report.request.write`，使用 HTTP Basic 客户端认证，不额外发送 audience 参数。随后 POST RequestURL，5 秒超时，不跟随 token 或业务重定向；TLS 验证不能关闭。

业务请求头：`Authorization: Bearer ...`、`Content-Type: application/json`、稳定 `Idempotency-Key`（Outbox EventID）、当前 UTC RFC3339Nano `X-Integration-Timestamp`、每次传输新随机 `X-Integration-Nonce`。同一个 EventID 的重试必须返回同一个下游 ID，不能重复创建申请。

JSON 为本地提交事务已经固化的实际载荷：

```json
{"request_id":123,"request_no":"REPORT-123","customer_id":45,"project_id":"project-id","report_type":"检测报告","reason":"申请说明"}
```

租户由签名机器 token 绑定，JSON 不携带邮箱或客户登录账号。成功响应必须为 HTTP 2xx、单个有界（不超过 1 MiB）JSON，且 `data.downstream_request_id` 是 1–128 字符的稳定非空 ID：

```json
{"code":"OK","data":{"downstream_request_id":"EXTERNAL-123"}}
```

失败必须用非 2xx 状态；错误 body 不写日志。取得 ID 之后仍需本地 `MarkApprovalStarted` 及 Outbox SENT 成功。中间失败时再次发送同一个事件，依靠外部幂等收敛。当前适配器不是任意 REST/SDK 的自动转换器。

## 状态与文件回调

上游 POST `${PortalPathPrefix}/internal/report-callbacks`（默认 `/customer-portal/internal/report-callbacks`），必须持有平台签发 `token_use=application`、当前租户、`report.callback.write` scope 的 EdDSA 机器 token；同时提供稳定 `Idempotency-Key`、当前 `X-Integration-Timestamp`、每次尝试独立 `X-Integration-Nonce`。Portal 独立 schema 持久化 nonce 防重放。时间窗口五分钟；回调的租户、客户、项目、下游申请 ID 必须匹配本地申请，version 单调，历史精确重放幂等，相同版本变载荷冲突。

```json
{"tenant_id":"tenant-id","request_id":123,"customer_id":45,"project_id":"project-id","version":2,"status":"ISSUED","downstream_request_id":"EXTERNAL-123","approval_result":"APPROVED","object_ref":"opaque-file-id","file_name":"report.pdf","mime":"application/pdf","file_hash":"64位SHA256十六进制","size":1024,"report_revision":1}
```

其他状态遵循已有 `SUBMITTED → APPROVING → APPROVED_PROCESSING/REJECTED → INGEST_PENDING → ISSUED`（失败回调为 `PROCESSING_FAILED`） 状态机；不可跳过审批上下文。ISSUED 回调仅把来源描述符加密排入摄取任务，可信文件摄取和本地事务完成前不会发布下载或通知。

`object_ref` 必须是平台 File Gateway 的 opaque file_id，不能是 http URL、本地路径或对象存储 token。已有外部报告服务应由受控发布桥将 PDF 通过 File Gateway V2 以 `project_management` 来源应用、`project.report` purpose、同租户 `PORTAL_REPORT_REQUEST/<request_id>` 精确 ACTIVE 绑定发布为 READY。外部服务不必是仓内 Project 模块，但来源发布客户端须由平台正式授权；Portal Worker 不能拥有 Project 的广泛文件写/读凭据。回调 size/hash/name/MIME 固定真实 PDF 内容；网关返回实际 immutable version。旧本地文件或缺可信发布绑定的旧回调不会降级放行。

## API 身份与中文字体

API 单独配置 `PORTAL_REPORT_IDENTITY_URL`（CRM 只读引用完整 URL）、`PORTAL_REPORT_IDENTITY_CLIENT_ID/CLIENT_SECRET`，token URL 使用 `PORTAL_CRM_INVITE_TOKEN_URL`，scope 固定 `portal.report.download_identity.read`。身份凭据仅注入 API；来源读取凭据仅注入 Worker。共享内容密钥须同时区别于敏感数据和描述符密钥。

`PORTAL_CJK_FONT_PATH` 指向已交付的 `/app/assets/fonts/UIPPortalSans-Regular.ttf`（镜像路径由部署装配确定），源资产位于 `assets/fonts/`，固定 Noto CJK 上游衍生、OFL-1.1 可再发行静态 TTF，配套 `OFL.txt`、`SOURCE.json`、`SHA256SUMS` 和重建说明。生产镜像或只读卷同时包含 TTF 与许可。TTF 文件 basename 必须等于字体内部 PostScriptName `UIPPortalSans-Regular`；不得只改名安装其他字体。不可把系统 Mac Arial 字体拷入服务器，当前不建议 TTC 或变量字体，不能只改扩展名转换字体。

`PORTAL_PDF_CONFIG_ROOT=/app/data/portal-pdf` 是 API 运行 UID 可写的私有目录，用于 pdfcpu 安装该字体后的配置/字体缓存。字体文件本身可只读；无需 Worker 安装字体或接触身份凭据。启动实际校验 PostScriptName、中文/ASCII cmap，下载再校验每个真实水印字符，缺字或字体损坏失败关闭，没有系统默认字体回退。镜像正式字体验收可设置 `UIP_TEST_CJK_FONT_PATH`、`UIP_TEST_CJK_FONT_NAME` 运行真实 PDF 水印回归，并用 Poppler 渲染检查五个完整字段。
