# 开发与运行说明

本业务域包含十六个 Go 进程：

- `cmd/crm-server`：客户、商机、售前和 Portal 邀请管理 API，默认监听 `:8090`；
- `cmd/portal-server`：独立客户 Portal API，默认监听 `:8091`；
- `cmd/presale-worker`：审批引擎与 PMS outbox 投递进程；
- `cmd/presale-engineer-sync-worker`：PMS 技术人员权威池同步进程；
- `cmd/presale-assignment-notification-worker`：售前指派 outbox 到个人站内消息投影进程；
- `cmd/presale-progress-notification-worker`：售前过程记录 outbox 到申请人/当前执行人个人站内消息投影进程；
- `cmd/presale-alert-worker`：TS-008 超时规则扫描与站内消息投影进程；
- `cmd/opportunity-alert-worker`：BM-002 商机阶段停留超时扫描与站内消息投影进程；
- `cmd/opportunity-owner-notification-worker`：BM-001 负责人交接 outbox 到 CRM 个人站内消息投影进程；
- `cmd/contract-transfer-worker`：BM-001 `OPPORTUNITY_SIGNED` outbox 到合同系统受理箱投递进程；每轮最多处理 `CONTRACT_TRANSFER_BATCH_SIZE` 条，但每条都独立领取并在完成后才领取下一条，租约不会因批量串行 HTTP 被后排事件耗尽；每次领取使用随机 fencing token，配置的 Worker ID 即使在同主机多副本间重复也不能让旧执行者写回新租约；
- `cmd/portal-project-worker`：项目快照同步进程；
- `cmd/portal-project-export-worker`：项目进度 PDF 异步渲染进程；
- `cmd/portal-report-worker`：报告申请 outbox 投递进程；
- `cmd/portal-feedback-worker`：客户反馈首次响应 SLA 扫描和内部待办投影进程；
- `cmd/portal-invite-compensation-worker`：Portal 邀请映射补偿进程。
- `cmd/portal-access-disable-worker`：Portal 访问禁用 Saga 自动恢复进程；使用数据库 `SKIP LOCKED` 有限租约，只持有 `portal.identity_mapping.disable` 与 `application_role.revoke` 两组相互独立的最小权限凭据，第 8 次失败进入可观测死信。

前端位于仓库级 `frontend` 工程，客户商机模块和 Portal 模块分别在 `frontend/src/modules/customer_opportunity`、`frontend/src/modules/customer_portal`。

## 数据库

使用 MySQL 8 和版本化 SQL，不执行 GORM `AutoMigrate`。CRM 与 Portal 使用两个独立 schema，必须严格按 [`migrations/README.md`](migrations/README.md) 的清单执行，不能把目录中全部 SQL 通配执行到同一个数据库。

## 本地运行

### 推荐：统一 Docker + 基础平台部署 Agent

在相邻 `platform` 目录启动基础服务后，仅当 `customer_portal/dev` 尚不存在时执行一次：

```bash
cd ../platform
bash scripts/subsystem.sh onboard \
  --preset customer-portal-local \
  --api-base-url http://localhost:8081/api/v1 \
  --platform-origin http://localhost:8081 \
  --account admins
```

该流程自动创建 Portal 独立 MySQL、执行 Portal migration、登记并发布 OIDC/权限目录、创建六个单 scope 服务 Client，写入 CRM/Portal 运行配置并启动 `portal-api`。外部客户预置同时创建 HUMAN/LOCAL 登录账号但不创建密码；平台 migration `000071` 为升级前记录补齐账号并建立数据库外键/唯一约束。平台管理员随后在账号管理中显式初始化只显示一次的临时密码。已有接入只执行 `bash scripts/docker-local.sh refresh-portal-api`，不能重复 onboard。完整说明见 [`../platform/docs/subsystem-onboarding.md`](../platform/docs/subsystem-onboarding.md)。

### 手工启动进程

1. 创建 CRM 与 `customer_portal` 两个数据库并执行各自 migration。
2. 为 CRM 配置 `.env.example`，为 Portal 配置 `.env.portal.example`，为各 Worker 配置对应 `.env.*-worker.example`。
3. 分别执行：

```bash
go run ./cmd/crm-server
go run ./cmd/portal-server
go run ./cmd/presale-worker
go run ./cmd/presale-engineer-sync-worker
go run ./cmd/presale-assignment-notification-worker
go run ./cmd/presale-progress-notification-worker
go run ./cmd/presale-alert-worker
go run ./cmd/opportunity-alert-worker
go run ./cmd/opportunity-owner-notification-worker
go run ./cmd/contract-transfer-worker
go run ./cmd/portal-project-worker
go run ./cmd/portal-project-export-worker
go run ./cmd/portal-report-worker
go run ./cmd/portal-feedback-worker
go run ./cmd/portal-invite-compensation-worker
go run ./cmd/portal-access-disable-worker
```

4. 在仓库 `frontend` 目录启动前端：

```bash
npm install
npm run dev
```

Vite 开发代理默认把 `/customer-opportunity` 转发到 `http://localhost:8090`，把 `/customer-portal` 转发到 `http://localhost:8091`。

## 健康检查与访问日志

CRM 和 Portal 分别提供 `<path_prefix>/healthz`、`<path_prefix>/livez` 和 `<path_prefix>/readyz`，均不经过登录。`healthz` 保留兼容语义并探测 MySQL；`livez` 只表示进程存活；`readyz` 在数据库连接失败或必需依赖未就绪时返回 503。部署网关必须按 CRM、Portal 的独立 `path_prefix` 配置三类探针；探针已纳入 OpenAPI 和 CI 路由契约。

`/customer-opportunity/healthz` 与 `/customer-portal/healthz` 只表示对应进程及核心数据库可用，不代表对象存储、病毒扫描、审批、PMS、报价/投标、公安提交等可选外部能力已经就绪。登录后前端分别读取 `/customer-opportunity/api/v1/capabilities` 与 `/customer-portal/api/v1/capabilities`；服务端只根据本地已注入适配器、启用开关和数据库中的新鲜 Worker 心跳返回稳定的 `available/mode/reason_code`，不在页面请求内探测远端，也不返回 URL、Client、scope、密钥等部署信息。能力查询失败时前端安全关闭相关按钮，实际写接口仍执行同一服务端检查并作为最终权威，不能依靠前端能力位绕过校验。

售前申请、Portal 报告申请和项目 PDF 导出只有在对应独立 Worker 存在新鲜数据库心跳时才接受新任务；已有幂等请求仍可安全重放。可选适配器或 Worker 未就绪不会使核心客户、商机、Portal 项目/报告状态查询随之失败，只会让关联动作明确返回 503。OIDC、机器身份验证、数据库以及 Portal 调 CRM 的邀请内部客户端属于相应进程的启动必需依赖；缺少这些正式配置时对应进程不会启动，不能把这种情况误判为“降级后仍完整运行”。

两个 HTTP 服务使用同一个安全结构化访问日志契约，每个请求只输出一条 JSON `http_request_completed` 事件：`request_id`、已认证的 `tenant_id/user_id`、固定 `module`、稳定 `error_code`、HTTP method、Gin 路由模板、status、duration_ms 和响应 bytes。路由字段使用 `/resources/:id` 模板，不记录原始路径；日志实现不会读取 query、header、body、Cookie、Authorization、IP、User-Agent 或 panic 值。恢复中间件也不再使用 Gin 默认的请求头转储，只记录 `request_id/module` 后返回统一的 `COMMON_INTERNAL_ERROR`。CRM 和 Portal 主进程均将默认 logger 初始化为 JSON `slog`，使访问日志及既有服务端错误进入同一结构化输出。

指标名称、标签基数、采集协议和 `/metrics` 暴露边界尚未签版；在基础设施确定由 Prometheus 拉取、OTLP 推送或统一 sidecar 采集前，本实现不猜测增加公网指标端点。当前 CRM 与 Portal 生产入口 OpenAPI 3.1 契约位于 `api/openapi/crm.yaml`、`api/openapi/portal.yaml`，执行 `go run ./cmd/openapi-check` 会解析真实 Gin 注册源并检查全部 path/method、防止路由漂移，同时检查 operation 的安全边界、`operationId` 和响应声明。外部 Provider 正式字段未签版的部分只标注 pending，不纳入本仓库入口契约。

## 身份与安全边界

生产 CRM 和 Portal 分别使用基础平台 OIDC Authorization Code + PKCE S256，并分别维护 HttpOnly 服务端会话 Cookie。两个 Cookie、OAuth Client、数据库和加密密钥不得复用。

CRM 与本地开发模式均要求：

- `DEV_AUTH_ENABLED=false`；任何环境设为 `true` 都会在监听端口前失败退出；
- 配置基础平台 Discovery issuer、CRM Client、租户、授权目录哈希和公开 Origin；
- 浏览器非安全方法必须同时携带匹配的 `Origin` 和 `X-CSRF-Token: 1`；
- `/api/v1/internal` 不接受浏览器会话，必须使用基础平台 `client_credentials` 机器 Token，并校验 audience、scope、`token_use=application`、时间戳和单次 nonce。

CRM 不接受任何客户端传入的用户、角色或权限请求头。本地联调也必须先通过基础平台 OIDC 登录；机器接口只接受基础平台签发的单 scope 应用令牌。

Portal 只保存 `OIDC sub ↔ customerId` 映射和本地会话，不保存客户密码，也不签发独立身份 JWT。密码、MFA、找回和账户锁定均由基础平台负责。

PO-08 账号安全接口位于 `/customer-portal/api/v1/account`：返回当前 OIDC sub 的脱敏标识、Portal 活跃会话和最小安全事件，并允许当前用户撤销自己的会话、确认异常事件。浏览器只接收随机 `public_id`，不会收到 session hash、Token、完整 IP 或其他账号标识。`PORTAL_ACCOUNT_SECURITY_CENTER_URL` 必须是可信 HTTPS 配置，Portal 只追加由 `PORTAL_PUBLIC_ORIGIN + PORTAL_PATH_PREFIX` 生成的 return URL，不接受请求覆盖。当前安全元数据使用 TCP 直连 IP 并忽略未配置可信代理的转发头；属地需要后续接入可信 GeoIP/网关来源。

Portal 调 CRM 校验/消费邀请使用独立的基础平台 `client_credentials` Client，最小 scope 固定为 `portal.invite.verify`。每次调用都携带 RFC3339Nano UTC 时间戳和新的 256-bit 随机 nonce，CRM 负责五分钟时钟窗口与防重放。当前基础平台 `/oauth2/token` 只接受 `client_secret_basic`、`grant_type` 和 `scope`，不接受非标准 `audience` 表单字段。CRM/Portal 的机器接口使用只读 Ed25519 公钥本地验证 application JWT；`MACHINE_TOKEN_ISSUER`/`PORTAL_MACHINE_TOKEN_ISSUER` 对齐平台 `AUTH_JWT_ISSUER`，audience 对齐 `AUTH_APPLICATION_JWT_AUDIENCE`，不能使用 OIDC Discovery/JWKS（该端点只发布 OIDC manager 公钥），也不得向子系统挂载平台私钥。当前 Docker 基线 audience 为 `basic-platform-application`，其他部署必须显式填写实际值。

## 客户组合查询

`GET /customer-opportunity/api/v1/customers` 支持客户号/名称、类型、行业、区域、负责人、状态、创建时间和最近跟进时间组合筛选，并支持 `NEW`（当前时间前 30 天）、`WON` 与 `FOLLOWUP_DUE` 快捷筛选。时间参数必须为 RFC3339，前端将日期范围转换为 UTC 半开区间；排序只允许 `created_at/updated_at/name/last_followup_at/opportunity_amount_sum`，并使用同方向客户 ID 保证稳定分页。未知 query key、非法分页、未知快捷筛选或排序均返回 400，不忽略调用方错误。

列表与全部详情子资源先按 tenant 及 SELF/ORG/ALL 范围验证客户可见性。联系人只返回脱敏值，本地商机页签只返回最小摘要，操作日志读取共享 append-only 审计流并要求 `customer.audit.read`。当前没有重点客户权威分类字段，因此 `KEY` 返回结构化 503。项目历史已通过独立 `portal.project_history.read` Client Credentials 从 Portal 本地同步投影读取，先校验 CRM 客户可见性，再返回严格分页的最小 DTO；`source_updated_at`、快照行 `synced_at` 与客户同步链路 `sync_last_success_at` 分离，`stale` 只描述同步链路，不声明上游实时。该适配器默认关闭，未配置时返回 503。XLSX 导出 Worker、加密对象存储和短效下载未配置时也分别失败关闭，不生成占位结果。

## 客户档案子记录

## 商机可信附件

`000049` 和 `internal/modules/opportunity/attachment.go` 实现只存对象引用的附件纵切面。浏览器先读取商机范围内的 capability；只有对象存储与扫描器同时可用才创建上传会话。服务端固定校验文件名、扩展名、MIME、20 MiB 大小和 SHA-256，并在上传完成时重新核对不可变 object version、真实大小、MIME 和摘要。状态固定为 `PENDING_UPLOAD → FINALIZING → SCANNING → CLEAN/REJECTED/SCAN_FAILED`；`FINALIZING` 是并发外部送检抢占状态，扫描器必须以附件 public ID 作为稳定业务幂等键。只有 `CLEAN` 可进入受控下载，下载前后分别写不可变审计；列表、capability、完成和下载都复用父商机 tenant 与 SELF/ORG/ALL 数据范围。

当前仓库没有满足该信任边界的生产对象存储/病毒扫描契约。基础平台现有本地文件服务在写入后直接标记可用，不能用于商机可信附件。CRM bootstrap 因此注入明确 unavailable adapter：在任何上传会话元数据写入前返回 `503 CRM_OPPORTUNITY_ATTACHMENT_UNAVAILABLE`，前端依据 capability 禁用上传；数据库不保存二进制，也绝不把未扫描文件标记为可下载。正式适配器必须经过 signed-upload、不可变对象版本、扫描 callback machine scope、幂等和恶意文件契约测试后才能启用。

商机当前团队与独立任期账本分开保存。`PUT /api/v1/opportunities/{id}/members` 在同一事务中关闭被移除人员或旧角色的活动任期，并为新增、重新加入或角色变化创建新任期；`GET /api/v1/opportunities/{id}/member-terms` 先复核父商机数据范围，再按人员、活状态和分页查询。`000053` 对迁移前数据仅返回 `snapshot_at` 与 `active_at_snapshot`；加入、移出时间及操作人是未知值，不使用可复用成员行的时间或通用审计 JSON 推测。

`POST /customer-opportunity/api/v1/customers` 强制要求 `Idempotency-Key`。服务端先校验 `customer.create`、规范化全部字段和联系人，再用部署 HMAC 处理电话、邮箱、统一社会信用代码后计算最终 SHA-256 请求摘要；数据库只保存最终摘要和首次脱敏公共响应，不保存敏感明文。客户号、客户及联系人、审计和创建重放记录在同一事务提交；重放记录绑定 tenant、actor 和客户创建人，精确重放不受后来同名客户或主档修改影响。幂等表唯一键或信用代码唯一键先发生的并发路径都只对白名单索引尝试 winner 查询，且只有 tenant/actor/key/载荷/资源/响应快照完整校验通过才重放；无 winner 时保留原业务冲突。同键异载荷、跨操作人以及响应快照/资源绑定异常均失败关闭。前端只在页面内存中按同一规范载荷复用键，服务端确认成功后才清除；已经实际发送但结果未确认的命令会跳过非权威的前端查重预检，直接使用原键让后端安全重放。整页重载后的模糊结果需先查询服务端状态。

`GET/PUT /api/v1/customers/{id}/stakeholders` 与 `GET/PUT /api/v1/customers/{id}/systems` 提供 CM-001 关键干系人和信息系统全量清单。读取先按 tenant 及 SELF/ORG/ALL 验证父客户可见性；写入要求 `customer.update`、父客户为 `ACTIVE`、匹配的客户 `version` 和非空 `reason`，并在事务中锁定父记录、替换子记录、递增客户版本和写入字段审计/共享审计。任何版本冲突均返回 409，调用方必须刷新后重试。

干系人电话/邮箱使用客户敏感字段 AEAD 密钥加密，响应和审计只包含脱敏值；更新已存在记录时省略相应字段表示保留密文，显式空字符串表示清空，禁止把带 `*` 的脱敏显示值写回。信息系统 `protection_level` 仅接受 `LEVEL_1`～`LEVEL_5`，含义固定为网络安全等级保护定级，不是已经删除的客户信用评级。财务扩展尚未完成字段归属和查看权限签版，因此本轮不实现也不伪造。

CRM 的客户、商机、售前和 Portal 邀请 JSON 写入口，以及 Portal 统一路由中的 JSON 写入口，均使用同一严格解码基线：默认上限 1 MiB，只接受一个 JSON 对象，拒绝空、`null`、数组、标量、未知字段、尾随内容和超限请求，同时执行 Gin binding 校验。错误响应只返回稳定通用错误，不回显解析器详情或请求体内容。

## 客户 Excel 导入

`POST /api/v1/customers/imports/preview` 只接收 `file` 和 `reason` 两个 multipart part，总请求和文件分别有硬上限。Handler 逐 part 有界读取，不调用会把大文件落到临时目录的 `ParseMultipartForm`。工作簿默认经过进程内 `CodeImportScanner` 和 `safexlsx.ParseWorkbook` 的双重有界校验：固定 XLSX ZIP 容器、成员数量/展开体积/压缩比、路径穿越、可执行脚本成员、表头、公式、单元格和行列上限均失败关闭；不依赖外部杀毒服务。`ImportFileScanner` 仍可作为受控扩展点注入，但不是默认可用条件。

扫描通过后，服务端仅解析真实 `.xlsx` OOXML：限制 10 MiB、1000 数据行、压缩展开量、条目数、列数和单元格长度，拒绝加密包、宏、外部关系、危险 ZIP 路径、DTD、公式和 CSV 注入前缀。表头固定为开发需求中的 10 个中文字段。原始工作簿不入库；可提交命令使用客户敏感字段的 AES-256-GCM 密钥加密，预览 DTO、审计和错误 CSV 不包含电话、邮箱或统一社会信用代码明文。

`POST /api/v1/customers/imports/{jobNo}/commit` 要求预览版本和 `Idempotency-Key`，只允许原租户和原操作人提交。每个 READY 行在独立事务中重新查重、生成客户号、创建客户并写审计，所以一行失败不会回滚已成功行。作业使用可过期接管的数据库租约；每行事务都会锁定作业、校验 token/版本并续租，旧执行器在接管后不能继续写入。`GET /api/v1/customers/imports/{jobNo}/errors` 只向原操作人返回 RFC 4180 CSV，并对可能的表格公式前缀做中和。

同一幂等键始终绑定 `jobNo + 原预览版本`。浏览器刷新后可以用新幂等键接管已过期的同一提交，但新的租约 token/作业版本会隔离旧执行器；已完成作业也只能以最初预览版本重放。不可导入行不持久化加密命令，READY 行在导入成功或失败后立即清空命令密文；错误 CSV 响应还强制 `no-store` 和 `nosniff`。

## 售前过程时间线

`POST /customer-opportunity/api/v1/opportunities` 同样强制要求 `Idempotency-Key`。客户可见性校验在重放查询之前，并在创建事务和唯一键竞态恢复中再次执行；请求摘要绑定规范化完整载荷和客户，首次公共响应作为带完整性摘要的 JSON 快照持久保存。客户锁、商机号、商机、审计和重放记录原子提交；精确重放返回首次快照，不因后续商机更新改变；同一操作人同键跨客户或异载荷固定冲突。键坐标按操作人隔离，另一操作人可独立使用相同不透明键，但无法命中原响应。前端按页面生命周期复用显式键，只在成功后清除并在提交时禁用重复点击。

`GET /api/v1/presale/requests/{id}/timeline` 和 `GET /api/v1/presale/requests/{id}/available-actions` 都要求 `presale.read`，并在查询子资源前复用售前详情的父资源范围校验。销售角色只能读本人申请，技术/实施角色只能读本人当前或历史参与任务，多角色用户取二者并集；无受支持 TS 角色的普通读取者失败关闭，不能通过猜测同租户 ID 读取时间线。

时间线在数据库内使用单条 `UNION ALL` 查询聚合申请创建、状态、审批结果、指派增减、进度和有效工时，按 `(occurred_at,type_priority,source_id)` 倒序 keyset 分页，不把全部子表读入内存。Cursor 使用 HMAC-SHA256 签名并绑定租户与申请 ID；篡改或跨任务复用均失败关闭。返回 DTO 不含审批意见、驳回/取消原因、联系人、服务地址、工时备注和幂等摘要；进度链接仅保留已通过服务端规则的 HTTPS 地址。

前端详情将主档/工时、权威可用动作与时间线分开加载；`available-actions` 失败时不回退到详情旧快照，而是安全关闭所有变更入口。指派、审批、取消、进度、工时或投递重试成功后重新读取主档、可用动作和时间线，不重置列表筛选/分页。`000035` 仅增加指派结束与工时创建的时间线覆盖索引，不回填业务数据；存量大表上线必须使用发布平台认可的在线 DDL，并监控 metadata lock 和副本延迟。

进度新增使用 `Idempotency-Key + 规范载荷摘要`持久防重。Service 在单一事务内先按租户锁定任务，校验权威任务版本和 `EXECUTING` 状态，再锁定当前指派集合并确认操作人仍是当前执行人后才写入，因此与取消/改派并发时不会在旧权限下追加不可变记录。同键同载荷返回原结果，同键异载荷返回 409；前端在失败重试时复用同键，只在载荷改变或成功后轮换。`000036` 只为新进度增加可空幂等元数据/租户唯一索引，不改写存量不可变进度。

## 售前列表与状态看板

`GET /api/v1/presale/requests`、`GET /api/v1/presale/board` 和 `GET /api/v1/presale/filter-options` 均要求 `presale.read`，且共用从会话 Actor 计算的 `requestScope` 和 Repository 筛选链。请求不接受 `scope=all` 扩权；销售只看本人申请，技术/实施人员只看当前或历史参与任务，多角色用户取两者并集，仅有读权限但无受支持 TS 角色时查询失败关闭；管理/审计角色按已配置范围读取。详情、工时和时间线复用相同角色口径，不能通过猜测任务 ID 扩大列表范围；所有 SQL 始终强制租户条件。

TS-001 创建申请的幂等处理在任何 key 查询之前先调用真实商机可见性适配器，新摘要绑定账号、商机与规范化载荷；事务内二次查询和租户唯一键竞态回收使用同一绑定复核。前端把商机 ID、枚举、去空白文本和 ISO 时间规范化后生成页面内重试签名，显式把同一键交给 API，并只在服务端确认成功后清除；提交期间禁止重复点击。键不进浏览器持久存储，因此整页重载后的结果歧义必须先查询服务端状态。TS-005 工时幂等会先锁定父任务并复核当前执行人，再查询租户键；摘要绑定账号、人员、任务和规范化载荷，精确重放在首笔工时已使任务自动完成后仍可返回原记录，跨账号/任务则统一 409 且不返回旧对象。

TS-001 联系电话普通详情始终只返回掩码。独立 `GET /api/v1/presale/requests/:id/contact-phone` 同时校验 `presale.contact_phone.read`、tenant 和当前/历史真实指派关系，或 `sales_director/team_lead/technical_lead` 明确管理角色；`auditor` 为显式拒绝。端点完成 AEAD 解密与掩码绑定校验后，先写不含明文的 `CONTACT_PHONE_VIEW` 隐私审计再响应；解密损坏或审计失败统一 503 并安全关闭。响应带 `Cache-Control: no-store, private`，Vue 仅在权限和服务端能力位同时成立时提供显式按钮，明文只保留在详情组件内存并在关闭或切换任务时清除。

TS-002 人工审批、TS-003 指派/改派和 TS-004 取消均要求调用方显式提供 `Idempotency-Key`。`000040` 的 append-only 协调记录绑定 tenant、父申请、操作人、操作类型、节点动作与规范化载荷摘要，并与对应 outbox、指派/状态更新处于同一事务；审批记录保存 `NODE_1_*`/`NODE_2_*`，所以节点推进后原节点操作人仍能精确重放已受理命令。竞态恢复只接受 `gorm.ErrDuplicatedKey` 或 MySQL 1062，其他数据库错误原样失败。前端在页面生命周期内按操作、任务和规范载荷分别保留所有结果未确认的键，切换或关闭详情不会丢弃；只有对应请求确认成功才清除，prompt 取消和前置校验失败不会分配键。键不写 local/session storage 或 URL，因此整页重载后的结果歧义仍需依靠服务端状态核对，这是非持久浏览器状态的明确边界。

TS-003 替换指派时会为每个新增/移出人员在同一事务追加 `crm_presale_assignment_events` 不可变证据及只含 evidence ID 的 outbox。`presale-assignment-notification-worker` 采用 SKIP LOCKED、租约接管、6 次退避和第 7 次死信；投影前按 tenant 重读任务、指派和证据并校验稳定事件 ID、人员、角色、时间和当前/结束状态，不信任 outbox 展示字段。个人收件箱把商机负责人通知的 `user_id` 与售前通知的 `person_id` 分开，数据范围不受 SELF/ORG/ALL 放大；`person_id` 缺失时失败关闭，不回退到 `user_id`。PMS 指派角色是业务角色，不猜测为 CRM OIDC 角色；具有 `presale.read`、权威非空 `person_id` 且存在真实当前或历史指派关系时，列表、详情和商机内嵌入口使用同一数据域。

TS-004 新增进度时会在锁定任务和当前指派的同一事务中写入进度、逐收件人不可变证据及只含证据 ID 的 outbox。可信收件人限申请人 CRM `user_id` 和发生时其他当前执行人的 PMS `person_id`，两个命名空间分离且作者只在同一命名空间内排除；没有管理层/组织目录契约时不猜测收件人。`presale-progress-notification-worker` 每条使用随机 fencing token 单独领取，重读并复核 tenant、进度、任务、发生时指派任期和稳定事件 ID，6 次退避后第 7 次死信，以 `(tenant_id,source_event_id)` 唯一约束幂等投影现有个人收件箱。本期不调用或声称送达外部 IM、邮件、短信。

基础平台已通过独立 migration `000068` 显式维护租户内唯一的 `pms_person_id`，并在 Access Token、ID Token、UserInfo 与 Discovery 中签发/声明 `person_id`；CRM `000058` 将该权威值持久化到服务端 OIDC 会话，15 秒 UserInfo 复核发现变化即撤销旧会话。平台维护端、JWT 校验端与 CRM 接收端均使用与 MySQL ASCII 列一致的 64 字节 grammar，绝不从 `sub`、平台用户 ID、账号或员工编号推导。缺少绑定时执行人范围、个人指派通知、进度、工时和 SELF 人员报表继续安全关闭；正式 PMS 人员目录和生产 OIDC Client 仍待联调。负责人/组织目录已签版并接通：平台机器接口 `GET /api/v1/internal/owner-directory`（`owner_directory.read` scope）只返回内部有效 OIDC 用户及其授权组织成员关系，CRM `GET /api/v1/owner-directory` 透传同一投影，客户/商机负责人写操作按该权威配对校验并失败关闭。

CRM 现有业务主表和 append-only 审计的 actor 字段统一为 64 字节，因此 CRM OIDC 在完成认证前明确拒绝超过 64 字节的 `sub`，不会建立“可登录但首次业务写入才失败”的会话。Portal 外部账号使用独立 schema 和 128 字节上限；两者不得互相推断。若基础平台未来签版更长的 CRM subject，必须先用单独 migration 扩展全部 CRM actor/created_by/updated_by 外键及索引，再放宽认证边界。

TS-002 已提供独立当前任务查询适配器：`available-actions` 和动作命令均使用专用 `presale.approval.task.read` Client Credentials，按 `engine_instance_id + current_node + authenticated approver` 实时解析权威任务；浏览器只提交 `PASS|REJECT + comment + version`，不接收或提交 `engine_task_id`。`000060` 把解析出的 task、approver、action 与动作 outbox 原子绑定；一个绑定等待回调期间不再发布或覆盖第二个动作，`available-actions` 也不暴露审批按钮。`000065` 为审批日志保存 engine instance 与 event sequence；首次回调和已处理重放都必须与实例、任务、序号、节点、审批人、结果、意见和发生时间完全匹配。审批当前任务、动作和 PMS 端点必须是 HTTPS，OAuth Client 分离且 scope 精确；所有客户端禁止跟随 HTTP 重定向，发送 RFC3339Nano 时间戳、每次 256-bit nonce，并严格验证 JSON Content-Type、成功信封及响应身份。PMS 的 HTTP `Idempotency-Key` 固定使用载荷内业务 `idempotencyKey=worklogId`，与 outbox 重试次数无关。正式审批 OpenAPI、Client、网关、mTLS 证书和真实任务环境未配置时，按钮和动作安全关闭。

CRM 与 Portal 的浏览器角色/权限目录现由 `internal/platformcatalog` 分别定义和校验，两个应用使用独立 catalog version、稳定 checksum、角色和发布凭据；业务机器 scope 不进入浏览器角色目录。生产启动会校验配置的 `role_config_hash` 与当前二进制内嵌角色-权限映射一致；CRM OIDC 还会把 Token 权限与全部有效角色的内嵌权限并集做精确集合比对，拒绝目录内但不属于当前角色的越权权限或缺失权限。`customer.duplicate.override` 为 HIGH 权限，仅授予 `sales_director` 与 `customer_admin`，普通 `sales` 仍不能覆盖疑似重名。开启目录发布后，通过基础平台正式 `client_credentials + authorization.catalog.sync` 契约幂等发布，Token 或目录发布失败均阻止服务启动，且 HTTP 重定向不会携带 publisher Secret/Bearer。部署前可运行 `go run ./cmd/authz-catalog crm` 或 `go run ./cmd/authz-catalog portal` 获取 `catalog_checksum` 及 `claims_role_config_hash`；本地部署 Agent 使用 `authz-catalog publish crm` 一次性发布，生产仍建议显式配置并签版。CRM 的 `OIDC_MAX_EFFECTIVE_ROLES` 必须与内嵌目录策略相同。`PLATFORM_AUTHORIZATION_CATALOG_*` 和 `PORTAL_AUTHORIZATION_CATALOG_*` 见各自 env 示例，浏览器、审计、Portal/CRM 机器调用凭据均不得复用。

基础平台现已在 Access Token、ID Token、UserInfo 与 Discovery 中签发/声明 `primary_org_id`、`organization_ids`；只包含当前有效直接任职，稳定排序、最多 100 个且不展开后代。CRM 已严格验签、持久化并在 15 秒 UserInfo 复核中比较组织集合；变化会撤销旧会话，`000055` 也会撤销缺少组织上下文的存量会话。该身份声明没有擅自改变现有角色范围策略：`sales_director/team_lead/technical_lead` 等仍按已确认的租户 `ALL`，其他角色仍按 `SELF`；要启用业务 `ORG` 范围，还需签版角色 scope 模式和售前任务组织归属契约后再修改全部查询适配器。

三个查询统一支持任务号、商机、申请人、当前/历史执行人、状态、场地、紧急度、申请/预期结束 UTC 半开时间区间、逾期和 PMS 推送状态；未知/重复/畸形 query、非法枚举、非法排序或反向时间区间均失败关闭。看板固定 7 个真实状态列，每列默认 20/最多 50 条并返回完整 total，不加载全表；筛选选项仅从当前可见的本地快照产生，每类最多 100 条并以 `truncated` 告知截断，不伪造平台人员目录。`000037` 增加 `(tenant_id,expected_end,status,id)` 索引，无数据回填；仍须以真实 MySQL 8 `EXPLAIN ANALYZE` 和生产规模数据验证 P95 ≤ 2 秒。

前端列表和只读看板使用同一组筛选参数，并把视图、分页、每列上限与筛选保存到 URL；不发送任何授权 `scope` 参数。看板只展示服务端返回的有界 items 和完整 total，不拖拽改状态。筛选选项失败时立即清空；列表/看板请求使用递增序列号拒绝过期响应和过期错误覆盖当前视图。

## 售前异步投递

### PMS 技术人员池

售前人员选择统一使用 `GET /api/v1/owner-directory` 的基础平台授权目录。历史 `GET /api/v1/presale/engineers` 与 `POST /api/v1/presale/engineers/sync` HTTP 接口已废弃并移除；底层人员快照和同步 Worker 仍作为内部投递/指派校验数据源保留。

`presale-engineer-sync-worker` 每 6 小时按租户调度，使用 OAuth Client Credentials 的单一 `technician.read` scope、强制 HTTPS、可选私有 CA/mTLS、禁止重定向、时间戳与随机 nonce、`FOR UPDATE SKIP LOCKED` 和有限租约。PMS 数据流 J 是共享权威技术人员池；本地 job tenant 只用于 CRM 缓存分区，不发送给 PMS，也不接受下游 tenant 覆盖。只有 HTTP 200、JSON Content-Type、完整非空、字段全部有效且角色枚举已知的快照才在单一事务内按 personId upsert，并在成功后停用快照缺席人员；HTTP、JSON、尾随内容、未知角色、重复 personId 或空快照均整批失败并保留旧缓存。联系方式只以独立 AEAD 密钥加密落库，API 不返回该字段。

TS-009 日报投影通过 `go run ./cmd/presale-report-aggregate-worker` 运行；发布校验或受控回填可追加 `-once`。必需 `MYSQL_DSN`，可选 `PRESALE_REPORT_AGGREGATE_TENANTS`（逗号分隔；空值时只从查询窗口内的权威工时和既有投影发现租户）、`PRESALE_REPORT_AGGREGATE_WORKER_ID`、`PRESALE_REPORT_AGGREGATE_POLL_INTERVAL`（默认 `1h`）、`PRESALE_REPORT_AGGREGATE_LEASE_DURATION`（默认 `5m`）和 `PRESALE_REPORT_AGGREGATE_LOOKBACK_DAYS`（默认 `32`，范围 1～366）。Worker 使用 UTC 半开日窗口和共享作业租约；每个租户的窗口在单一事务中完整替换，运行失败不会提交部分聚合。该表是可重建投影，当前 API 仍读取权威明细，正式切换必须先签版投影新鲜度和降级策略。

T5 J 的遗留示例时间 `2026-07-23 08:00:00` 未携带时区；首版固定按 UTC 解读，同时接受 RFC3339/RFC3339Nano，绝不使用服务器本地时区。PMS 正式契约升级时应统一改为 RFC3339 UTC。

Worker 使用 `FOR UPDATE SKIP LOCKED` 和有限租约领取 outbox，多实例间不会重复持有同一事件；过期的 `PROCESSING` 记录可以被其他实例恢复。对审批引擎和 PMS 的请求使用各自独立的 OAuth Client Credentials；审批命令使用稳定 `Idempotency-Key=event_id`，PMS 工时使用跨投递实现稳定的业务键 `Idempotency-Key=worklogId`，并强校验它与载荷 `idempotencyKey` 一致。

失败策略是首次投递后最多重试 6 次，退避窗口依次为 1 分钟、5 分钟、15 分钟、1 小时、3 小时、6 小时；第 7 次发送仍失败进入 `DEAD_LETTER`。PMS 投递失败只更新工时投递投影，不回滚已登记工时或任务自动完成状态。

`presale-alert-worker` 默认每 10 分钟扫描，使用数据库租约保证多实例只有一个扫描者，采用主键游标处理全部活跃任务。审批/指派时限从状态日志进入时间计算，执行提醒从 `expected_end` 计算，全部使用 UTC。收件人具有显式命名空间：当前执行人使用 PMS `PERSON/person_id`，申请销售使用 CRM `USER/user_id`；团队负责人和节点 1 销售总监只从本地持久化、未撤销且未过期的 CRM OIDC 会话中按基础平台签名角色解析为 `USER`，不再把 `sales_director` 当作 PMS 人员角色。列表和已读仅精确合并当前 actor 的 `USER/user_id` 与非空 `PERSON/person_id`，忽略 SELF/ORG/ALL 数据范围且绝不推断两种 ID 相等。预警先与 `PRESALE_ALERT_SITE_MESSAGE` outbox 同事务落库，再由 Worker 把 outbox 投影为本系统未读站内消息；本期不调用 IM、短信或邮件。任务状态、规则版本或当前接收人集合不再适用时会取消仍未投递或未读的旧预警。`000071` 不猜测存量 `recipient_id` 的来源：旧行标记为不可查询的 `LEGACY_UNKNOWN`，仅取消尚未投影的旧 PENDING 记录和 outbox。审批引擎当前未提供实际任务审批人目录，因此无法验证实际当前审批人时仍失败关闭；本地角色提醒不等同于已通知当前审批人。

本地 Docker 已将该进程作为独立服务 `customer-presale-alert-worker` 集成到 `platform/compose.local.yaml`，默认随客户与商机服务启动；生产 Compose 也使用同名独立服务。需要单独重建或拉起时，在 `platform` 目录执行：

```bash
bash scripts/docker-local.sh start-presale-alert-worker
```

查看状态和日志：

```bash
bash scripts/docker-local.sh ps
bash scripts/docker-local.sh logs --tail 100 --no-follow customer-presale-alert-worker
```

`opportunity-alert-worker` 默认每 10 分钟按主键游标扫描，仅计算 `初步接触/需求沟通/方案制定/报价/投标` 五个推进阶段，UTC 起算点固定为商机的 `stage_changed_at`。租户管理员以 `opportunity.alert.config` 维护每阶段小时阈值；每次修改生成新的配置版本，预警唯一键为租户、商机、阶段、阈值版本和收件人。Worker 使用全局数据库租约，并在每个商机处理前、每页结束和每批站内投影前后按 owner/expiry 续租校验，失去租约即停止本轮；若单个商机事务本身运行超过租期，第二实例仍可能接管，但商机行锁、数据库唯一键和 outbox 唯一键会阻止重复业务结果。当前可验证的权威收件人仅为商机当前负责人；团队成员、组织负责人和管理层目录契约尚未交付，因此 Worker 对这些对象失败关闭，不推测人员。阶段变化、进入终态、作废或负责人变化会在业务事务内取消仍在排队或未读的旧预警。预警和 `OPPORTUNITY_STAGE_ALERT_SITE_MESSAGE` outbox 同事务写入，Worker 使用 `FOR UPDATE SKIP LOCKED` 把它投影为 CRM 未读站内消息；本期不声称已送达短信、邮件或 IM。扫描错误会令进程退出，由部署 supervisor 重启并触发租约过期接管。

`opportunity-owner-notification-worker` 只消费 `OPPORTUNITY_OWNER_CHANGED_NOTIFICATION`，以有限 outbox 租约和 `FOR UPDATE SKIP LOCKED` 支持多实例及过期接管。投影前不信任 payload，而是锁定并复核同租户商机、匹配版本的不可变 `OWNER_CHANGE` 审计、新旧负责人角色及是否存在 `id > matched_audit_id` 的后续交接；已被后续交接覆盖、资源失效的事件进入 `CANCELLED`，确定性伪造或审计不匹配进入 `DEAD_LETTER`，临时数据库错误最多退避重试 6 次。数据库 `(tenant_id,source_event_id)` 唯一键保证重放只产生一条站内信。CRM 以 `/api/v1/notifications`、`/api/v1/notifications/unread-count` 和 `/api/v1/notifications/{id}/read` 提供最小个人收件箱；即使用户具有 ORG/ALL 商机数据范围，也只能查看和已读自己的消息。本期不调用或声称已送达外部 IM、邮件、短信。

## 售前投入报表

CRM 以 `GET /api/v1/presale/reports/summary|trend|distribution` 提供最长 366 天的实时统计，要求 `presale.report`。时间输入必须是 RFC3339，服务端转为 UTC 后使用 `[from,to)`；投入和趋势以有效 `worklog.work_start` 为时间字段，工时直接汇总标准小时。覆盖率按商机/售前任务与周期相交统计，周期前已创建但周期内仍有效的任务会纳入；PMS 最终成功率也按 worklog 周期统计，outbox 只用于确认投递任务已创建，不因延迟创建再次做时间过滤。`*_rate_percent` 为百分比字符串，例如 `50.00` 表示 50%。SELF 的覆盖分子/分母统一限定本人参与，ORG 只能在认证组织范围内收窄；显式 `person_id` 在所有指标中都定义为该人员对同一任务在周期内存在有效工时。

异步导出所需的导出 Worker、对象存储和受控下载链路尚未配置，因此 `POST /api/v1/presale/reports/exports` 当前明确返回 `503 CRM_PRESALE_REPORT_EXPORT_UNAVAILABLE`，不会伪造任务、URL 或文件。

## Portal 邀请限制

`PORTAL_INVITE_ENABLED` 在未接入环境中默认关闭；`customer_portal/dev` 部署 Agent 成功后会与 `PLATFORM_EXTERNAL_IDENTITY_ENABLED` 一起开启。邀请 token、统一信封 verify/consume、Portal→CRM OAuth 机器调用、Portal 映射和补偿落库已实现。生成邀请强制调用方提交 `Idempotency-Key`，并在首次外部写前持久化 actor/customer/request 绑定的 `crm_portal_provision_operations` saga；联系人恢复快照和可重放的一次性 token 只以 AES-GCM 密文保存。平台预置、角色分配、Portal mapping 和最终本地邀请分别推进不可跳级阶段，响应丢失后只复用同一操作和同一步远程幂等键，不重复已确认步骤；最终邀请、身份链接、token 密文和审计同事务提交。补偿任务复用对应远程键，Portal 映射重试不会用可变联系人 PII 重建身份，空 display name 也不会擦除既有展示名。CRM 已接入相互隔离的基础平台消费者，精确使用 `external_user.provision`、`application_role.assign` 与预留的 `application_role.revoke`，并把 Portal mapping 主链和补偿 Worker 接入私有 CA/mTLS、禁止重定向、严格信封和请求/响应身份绑定；Portal 内部预置的 tenant 只取已验签 machine principal，body 跨租户固定 403。

基础平台 migration `000069` 及 `docs/external-customer-identity-openapi.yaml` 已提供 `/api/v1/internal/external-users`、`/internal/application-roles`、`/internal/application-roles/revoke`。Provider 绑定 application JWT tenant/client、精确 scope、时间戳、nonce、稳定幂等、联系人摘要/手机号密文、Portal 应用角色目录、policy revision 和事务内审计；预置创建 `iam_user`、`iam_external_identity(PENDING_ACTIVATION)` 和一个没有 password credential 的 HUMAN/LOCAL 登录账号；CRM 可向有权管理员展示登录账号，但不能接触密码。平台管理员必须通过账号生命周期显式初始化一次性临时密码。Token、Secret 和密码均不由 CRM 或 Portal 生成、存储或返回。

邀请 `revoke`/重发只管理一次性链接，不等同于禁用 Portal 访问。独立的 `POST /api/v1/customers/{id}/portal-access/disable` 已实现 HIGH 权限、客户范围、操作人/客户/原因幂等绑定、客户行锁和身份链接 ID/版本快照。流程先调用 Portal 精确禁用映射并退出该 subject 的全部会话，再在 CRM 终态化链接和撤销待用邀请，最后使用独立 `application_role.revoke` Client 回收基础平台角色。CRM 映射的 `DISABLED` 是终态，旧开通 Saga 和旧 mapping 补偿均不能把它写回 PENDING。请求和 `portal-access-disable-worker` 共享有限租约；Worker 用 `FOR UPDATE SKIP LOCKED` 恢复到期 `RETRY_WAIT` 或崩溃租约，复用操作号派生的稳定远程键，第 8 次失败进入 `DEAD_LETTER` 并保留安全错误码。状态接口和前端高风险确认区展示当前阶段、下次重试和死信，且不返回原始平台 subject。正式 OAuth Client、网关、证书仍需交付后才能启动 Worker。

## Portal 报告异步投递

`portal-report-worker` 从 `customer_portal.portal_report_outbox` 使用 `FOR UPDATE SKIP LOCKED` 和有限租约领取申请，使用独立 `report.request.write` OAuth Client Credentials 调用项目服务，稳定发送 `Idempotency-Key=event_id`、RFC3339Nano 时间戳和随机 nonce。平台 token 请求使用 `client_secret_basic`，表单只含 `grant_type` 与 `scope`，不发送 `audience`。

项目服务的 ISSUED 回调不再在数据库事务中执行对象读取、病毒扫描或信封加密。Portal 仅验证白名单描述符，将其以独立 `PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64`（不得与通用加密 key 复用）的 AES-256-GCM 加密后连同摘要和稳定事件号原子写入 `portal_report_ingest_jobs`，状态先进入 `INGEST_PENDING`。同一 Worker 通过 `FOR UPDATE SKIP LOCKED`、有限租约和六次退避领取作业，正式 provider 必须以 `event_id` 幂等执行；只有 `CLEAN`、不可变对象版本、AES-256-GCM key ref、扫描编号/时间和 SHA-256 全部成立时，才原子发布文件、`ISSUED` 时间线及个人通知。当前 Provider 未交付，作业会重试；第七次失败将作业和报告原子转为 `DEAD_LETTER`/`PROCESSING_FAILED`，不会错误变为可下载。

失败后最多重试 6 次，退避窗口为 1 分钟、5 分钟、15 分钟、1 小时、3 小时、6 小时，第 7 次失败进入 `DEAD_LETTER`。项目服务接受后才把本地状态从 `SUBMITTED` 投影为 `APPROVING`；租约过期事件可被其他实例恢复，并继续使用同一 `event_id` 幂等键。

报告回调已使用平台只读 Ed25519 公钥本地校验基础平台 application JWT（`AUTH_JWT_ISSUER`、部署级 audience、`token_use=application`、租户、`report.callback.write` 最小 scope），并校验 RFC3339Nano 时间戳与单次 nonce、业务幂等键、版本、下游请求号、客户/项目归属、PDF 类型、50 MiB 大小、SHA-256 和受限 object ref；旧版本忽略，同版本不同载荷拒绝。文件证据必须同时具备不可变 object version、AES-256-GCM/key ref、精确 `CLEAN`、scan reference/time 和规范 SHA-256，历史行不会伪造 CLEAN。生产下载强制可信风险策略和逐次水印，`000061` 只保存租户/客户/账号/报告绑定的追踪码摘要用于反查，不落明文。可信 reader、解密、扫描、风险和水印 Provider 未配置时明确返回 503。

`000038` 为报告创建、审批启动和有效回调增加 append-only 状态事件。聚合状态更新与对应事件写入使用同一数据库事务；回调事件按报告全生命周期保存来源键摘要和载荷摘要，相同键/相同载荷精确重放为 no-op，相同键/不同载荷返回 409，旧版本或冲突回调不会追加重复/虚假事件。机器回调还强制基础平台 Principal 的 tenant 与请求体 tenant 完全一致。`GET /customer-portal/api/v1/reports/{id}` 同时按当前 Portal 会话的 tenant/customer 读取报告和按序事件，浏览器最小 DTO 只包含事件类型、序号、前后状态和发生时间，不暴露 actor、请求 trace、来源键或载荷摘要。迁移刻意不为存量报告合成历史，因为无法可信恢复历史 actor、来源和发生时间；因此升级前已有报告可能返回空时间线，这不代表状态被伪造补齐。`000038` 已通过 MySQL 8.4.11 全新 schema 验证；从上一版本升级时仍须为已有报告表在线增加唯一索引，并监控 metadata lock 与副本延迟。

`000039` 增加报告下载 grant 和 append-only 下载审计。`POST /customer-portal/api/v1/reports/{id}/download-grants` 仅在 Cookie 会话同时具备 `report.download`、同源 Origin、CSRF 头和幂等键时生成最长 72 小时 grant；同一账号对同一报告刷新会锁定父报告，在同一事务撤销旧 ACTIVE grant，ACTIVE slot/issue key 唯一约束兜底并发。明文 token 只在创建成功响应返回一次，数据库只存 SHA-256 摘要；重放同一发放键时不能恢复明文，因此固定返回 409。`POST /customer-portal/api/v1/reports/{id}/downloads` 仍要求同一会话/权限/同源/CSRF，token 只能位于 `X-Report-Download-Token`，禁止放入 URL、JSON 请求体或 `Authorization`。授权按 tenant/customer/request/account 复合范围查询；过期、冻结、撤销、依赖失败和不完整流均追加最小审计。无效 token（含非法长度）不保存 token 或其摘要，并以账号+报告+小时的不可逆桶唯一去重，避免已登录账号用任意猜测无限放大审计表；审计写入或真实依赖失败时下载失败关闭。不存在或越权的报告无法生成满足外键范围的审计，此时与其他无效凭证统一返回 404，不能通过 404/503 差异枚举报告 ID。

`000043` 增加报告发放站内通知。只有可信文件 `Ingest` 成功后，ISSUED 回调才在文件记录、报告状态、不可变状态事件同一事务内为原申请 `account_id` 创建唯一 UNREAD 通知；摄取失败和回调精确重放不会生成通知。通知列表、未读数和幂等已读按 Portal 会话中的 tenant/customer/account 三重隔离，首次已读追加不可变审计；这只代表 Portal 站内通知，不代表邮件、IM 或外部渠道送达。

当前下载实现不会把解密明文落临时文件。授权事务取出的数据库文件元数据会在申请缓冲槽和调用 reader 前重新限制为 `0 < size <= 50 MiB`；可信 reader 返回后，服务端在该上限内先将内容读入内存并重新校验实际长度与 SHA-256，校验通过后才提交 HTTP 200。进程内固定最多 2 个并发缓冲槽，等待受请求 context 取消，内存主体上界约 100 MiB（不含 Go/runtime 和 reader 自身缓冲）。传输成功后才在一个事务递增次数并写成功事件；客户端中断或成功审计失败会经独立短超时补记并进入服务端错误日志，但响应已经提交时 HTTP 状态无法再改写，这是明确的剩余语义。生产仍必须接入真正遵守 `OpenVerified` 契约、在 context 取消后解除阻塞的对象存储读取、信封解密和水印适配器，并按实例内存/副本数设置网关下载并发限制；当前 `unavailableReportFileReader` 保持明确 503，不能视为真实下载闭环。`000041` 只把报告申请和 grant 的 `created_by/updated_by` 扩到 128 字节，以对齐 Portal OIDC account/subject 标识上限，仍须验证在线 DDL 与回退前的数据长度条件。

Portal 前端报告中心已接入当前客户真实项目选择、申请、列表、详情和按事件序号展示的中文状态时间线。`report.request`、`report.read` 与 `report.download` 独立门禁；相同规范申请载荷失败重试复用同一 `Idempotency-Key`，字段变化才生成新键；列表、项目和详情各自具备加载/失败/空态及请求代次防旧响应覆盖。安全下载只在 `ISSUED` 状态可用；可信文件 reader 未配置时后端在授权校验后明确返回 503、记录失败审计且不生成伪文件。

Portal 内部账号映射入口同样使用基础平台 application JWT。开通和禁用分别只接受单一 `portal.identity_mapping.provision`、`portal.identity_mapping.disable` scope，并进一步将令牌 `sub` 绑定到 `PORTAL_CRM_PROVISION_CLIENT_SUBJECT`、`PORTAL_CRM_DISABLE_CLIENT_SUBJECT`；scope 相同但 client subject 不同也失败关闭。CRM→Portal 调用端使用独立 `client_credentials` Client、`client_secret_basic`、固定 scope、RFC3339Nano 时间戳和 256-bit 随机 nonce；账号映射和报告回调共享验签机制，但不得共享 OAuth Client。Portal 重放记录独立存入 customer_portal schema 的 `portal_machine_request_replays`。

## Portal 客户反馈

客户反馈接口位于 `/customer-portal/api/v1/feedbacks`，浏览器只使用 OIDC 服务端会话中的 tenant/customer/sub，不能通过请求覆盖数据范围。客户可创建异议、投诉、建议，查看本人列表/详情，追加沟通并在解决后关闭。期望联系方式使用 Portal AEAD 加密落库，浏览器仅收到脱敏值；客户 DTO 不返回内部数字 ID、tenant/customer/account、操作人、request_id、密文或幂等摘要，内部备注也永不进入客户 API。

浏览器权限严格分离：`feedback.create` 只允许创建，`feedback.read` 只允许列表/详情，`feedback.reply` 只允许已授权账号对可见反馈追加消息和确认关闭；后端路由、OIDC 白名单、Portal 导航、加载和操作按钮使用同一口径。仅 create 的账号创建成功后不会越权刷新列表；无权限直达页面显示显式空态而不发起业务请求。`account.security.manage` 只保护当前账号安全摘要、会话撤销和事件确认，不被任一 feedback 权限替代。

客户确认关闭强制携带 `Idempotency-Key`。后端先按 tenant/customer/account 读取可见反馈，再用租户级唯一键绑定反馈、actor 类型、账号、动作和规范化载荷摘要；精确重放不追加第二条状态日志或第二个 outbox，同键跨反馈、跨账号、跨操作人类型或异载荷固定返回 409。处理端所有状态动作使用同一租户级键口径，初始/system 状态日志的空键不参与唯一约束。前端在页面生命周期为同一反馈保留关闭键，只在确认成功后清除，避免响应丢失后的新键重复提交。

处理端仅开放基础平台 application JWT 的单一 `portal.feedback.manage` scope，使用时间戳和 nonce 防重放，不能由 `portal_customer` 浏览器会话调用。受理、回复、索要补充、内部备注、解决和驳回均有状态约束、持久幂等记录、状态日志和 outbox。该机器 scope 仍需基础平台正式登记；内部处理系统的最终归属和独立客服工作台尚未确认。

`portal-feedback-worker` 使用数据库租约、`FOR UPDATE SKIP LOCKED` 和 `(tenant_id,feedback_id,level)` 唯一键扫描 24 小时未首次人工响应的反馈，生成一次升级记录、Portal 内部未读待办和 outbox。当前不伪造外部 IM/邮件/客服平台成功；管理层外部通知适配器未接通。附件表和入口也暂不开放，直至可信上传、对象存储和病毒扫描契约交付。

## Portal 服务评价

服务评价浏览器接口为 `GET /customer-portal/api/v1/projects/{id}/evaluation-eligibility`、`POST /customer-portal/api/v1/evaluations` 和 `GET /customer-portal/api/v1/evaluations/{id}`。它只接受当前 Portal 会话的 tenant/customer/OIDC sub，不接受请求覆盖数据范围。首版 fail-closed：只有项目同步快照的权威状态严格等于 `COMPLETED` 才可评价，不猜测中文或其他相似终态。提交事务通过 `FOR SHARE` 锁定项目快照，与项目同步的 `FOR UPDATE` 串行，再写每项目唯一评价。四项分数均为 1～5，平均值以整数总分确定性计算并返回两位小数；评语作为长度受限的安全纯文本保存，前端不使用 HTML 渲染。

任一维度不高于 2 分或总分不高于 8 时，同事务生成唯一低分告警、Portal 内部 `UNREAD` 待办和 outbox。处理端只接受单一 `portal.evaluation.read` application scope，可按未读状态分页查看租户内低分待办并幂等标记已读；DTO 只返回评价公开号、项目、四维分值、平均分和评语，不返回客户、账号、数字主键、幂等摘要或审计操作者。该 scope 和浏览器 `evaluation.create/read` 权限仍需基础平台正式登记，负责人目录契约已签版但评价低分告警的管理层接收人尚未接入该目录，外部 IM/邮件适配亦未接入，因此不声称外部消息已送达。

机器统计接口仅返回整个租户的匿名聚合，样本少于 5 时返回不可用；不接受项目、客户或账号下钻筛选，避免调用端组合出可识别小样本。

## Portal 项目详情

项目列表、详情和动态均从 Portal 服务端会话取得 tenant/customer 数据范围，不接受浏览器覆盖客户标识。详情仓储和 DTO 只装载项目快照、里程碑、团队及脱敏联系方式，不查询动态；响应为旧客户端保留固定空数组 `activities: []`。动态只由 `/projects/{id}/activities` 返回：先按同一 tenant/customer 证明父项目可见，再用默认 `page=1/page_size=20`、最大 100 和 `(occurred_at DESC,id DESC)` 稳定分页。项目列表、详情和动态拒绝未知或重复 query；页码非正整数或 page_size 越界返回 400，不静默归一化。前端将详情、动态和评价拆分为独立请求状态，任一评价权限不足或评价服务失败均不会遮蔽项目主数据；评价资格允许 `evaluation.read` 或 `evaluation.create` 任一能力读取，但评价详情与提交仍分别受精确权限控制。

项目 ID 按项目服务产生的 opaque identifier 处理。前端逐段 percent-encode，Portal Router 使用 Gin RawPath 在单段路由匹配后解码，因此 ID 中的 `/` 不会被误判成路由层级；生产反向代理也必须保留 encoded slash，若代理提前解码会失败为 404。解码后的 ID 只进入 tenant/customer/project 参数化查询，不用于文件系统路径。

项目进度 PDF 由 `POST /projects/{projectID}/exports` 捕获 tenant/customer/account/project 绑定的不可变快照并持久入队；独立 `portal-project-export-worker` 以可接管租约和已签版 CJK 字体生成真实 PDF，长内容跨页。成功文件通过账号绑定的一次性 token 下载；若连接中断，前端复用原任务申请新 token。Worker 配置见 `.env.portal-project-export-worker.example`。

“站内联系项目经理”使用独立 `portal_project_conversations/messages/events/reads`，不混用客户反馈。项目同步新增向后兼容 `manager_portal_account_id`；空值关闭入口，姓名、`person_ref` 和脱敏联系方式均不可用于投递。客户使用 `project.message.read/send`；经理端由项目系统以单 scope `portal.project_message.manage` 的机器身份调用，并声明权威 Portal 账号。详情、发送、已读和幂等重放均在事务内锁定会话、客户账号与权威项目快照，经理切换后旧会话停止读写。消息为纯文本、最多 2000 字，单会话单发送账号每 5 分钟最多 10 条；第一页返回最新 100 条，更早记录使用同会话不透明消息游标的 exclusive keyset 分页，不使用 OFFSET。`MESSAGE_ACCEPTED` 只表示 Portal 本地持久化；`000054` 对每条真正显示且投递给当前读取账号的消息保存独立回执并写 `MESSAGE_READ_RECORDED`，不能用高水位跨过未展示旧消息。客户前端只在消息成功显示后批量确认当前页面内的经理消息，回执失败不隐藏正文。原 `000052` 高水位表仅保留为兼容证据，升级时只把其精确目标迁入逐消息回执，不推断更早消息已读；仍需项目系统正式同步/机器工作台 UI 联调、上一版本升级演练、浏览器 E2E 和性能验收。

## Portal 等保备案核心

CUS-004 浏览器接口位于 `/customer-portal/api/v1/filings`。客户数据范围完全取自 OIDC 服务端会话，任何请求都不能传入或覆盖 tenant/customer；备案创建、列表、详情、7 个 section 暂存、2 张等级矩阵、全量校验和提交均按 tenant/customer 复合条件读取。`filing.read/create/update/submit` 为 Portal 授权目录中的独立权限。

首版表单固定为 `form_version=2025.1`，支持原型字段的明确安全子集。7 个 section code 为 `ORGANIZATION`、`CLASSIFIED_OBJECT`、`CLASSIFICATION`、`NEW_TECHNOLOGY`、`MATERIALS`、`DATA_INVENTORY`、`CLASSIFICATION_REPORT`；服务端对每个 section 使用固定 key 白名单、JSON 类型、长度、枚举、必填和条件必填规则，拒绝未知字段及尾随内容。草稿按 section version 乐观锁；矩阵固定为 `BUSINESS_INFORMATION`、`SYSTEM_SERVICE`，每个 code 只有一条可撤销选择记录，行列均为服务器白名单。表单全文（含联系人等个人信息）、幂等回放响应和提交文档均使用 Portal AES-256-GCM 加密落库；提交哈希针对固定 section/matrix 顺序的明文 canonical JSON 计算。

提交事务以 `FOR UPDATE` 锁定备案头，重新解密并全量校验，生成永久不可变快照后把 Portal 内部状态置为 `WAITING_CONTRACT` 并阻断草稿写入；`SUBMITTED` 专门保留给未来可信公安回执。管理员解锁只开放机器接口 `POST /customer-portal/internal/filings/{id}/unlock`，仅接受单一 `portal.filing.unlock` scope、显式 customer_id、版本、理由和幂等键，可把等待契约或已确认记录恢复为 `DRAFT` 并追加审计。旧提交快照不会被解锁或后续修改覆盖。

`MATERIALS` 已提供受控创建上传、对象直传、完成核验和机器扫描回调；只保存加密对象引用、不可变版本、SHA/MIME/大小和扫描证据，且并发同幂等键会严格重读赢家，不泄露唯一键错误。前端已接真实上传与扫描状态。默认对象存储和扫描适配器为 unavailable，因此正式 Provider 未注入时返回 503；身份证、动态子系统清单等未签版字段、正式 2025 模板、PDF 生成及公安外部提交仍不臆造，`POST /filings/{id}/exports` 继续明确返回 503。

> **范围说明（2026-08-05，已确认）**：客户自助门户**不涉及与公安相关的对接**。本段及下文“公安提交 Worker / 回执 / SUBMITTED”属于超出门户范围的既有实现，按边界保留；门户验收与后续开发不再以公安对接为准，正式公安 Provider 契约与门户无关。

公安提交已实现 provider-neutral Worker 核心。它只领取 `WAITING_CONTRACT` 的不可变快照，原子切换为 `SUBMITTING`，解密并复核 canonical snapshot 摘要及所有材料的不可变对象版本、SHA-256、`CLEAN` 扫描编号和时间，再以稳定 `event_id` 调用正式 Provider。Provider 必须对该事件幂等返回同一已验签回执；回执证据在落库前计算明文 SHA-256 并加密。只有回执、备案状态和 outbox 在同一事务提交成功后才使用 `SUBMITTED`，七次失败后进入 `SUBMISSION_FAILED`，不会把本地快照误称公安已受理。正式 2025 请求 schema、签名验签、回执格式和 Provider 未签版，因此当前不提供会伪造外部成功的可运行适配器。

## 当前外部依赖缺口

- 基础平台外部用户预置/Portal 角色分配/回收 Provider 与 OpenAPI，以及 Portal 激活后 CRM 邀请 `USED` 与身份链接 `ACTIVE` 的原子回写已完成；本地 Agent 已补齐 OAuth Client、scope、网关、数据库和运行配置。生产仍需 HTTPS、Secret 管理、mTLS、外部客户交付流程和正式端到端验收；
- 报价/投标主动查询与最长 5 分钟固定地址签名调起已实现；主动查询 OAuth/token/resource 与浏览器固定调起地址均强制 HTTPS，查询客户端拒绝重定向并严格校验 JSON/envelope/商机绑定。正式 Client、mTLS 证书、URL、外部验签、nonce 单次消费及 key rotation 仍待签版。`OPPORTUNITY_SIGNED` 已由独立 Worker投递到合同系统幂等受理箱；合同端核对队列不创建合同、不启动审批。数据流 L 已实现精确 `contract.summary.read` 和严格 envelope，正式环境仍待联调；
- 平台人员/组织目录的商机预警团队与管理层收件人解析契约；
- PMS 人员同步正式环境、共享权威池空快照确认和无时区时间字段升级；OIDC `person_id` 权威绑定与签发代码已完成；
- Portal 项目服务正式环境和报告回调 machine JWT；
- Portal 报告正式对象 reader、信封解密/文件加密、水印及可信风险网关/GeoIP Provider；安全证据、追踪摘要、download grant、审计和受控路由已实现，Provider 未配置时 503。
- Portal 备案正式 2025 全字段/schema 与模板签版、正式对象存储/扫描 Provider、PDF 渲染和公安外部提交/回执契约；本地材料纵切面、等待/提交中/失败状态机、稳定投递事件和加密回执持久化已实现。

完整逐需求状态见 [`开发状态.md`](开发状态.md)。

## 演示数据初始化

`go run ./cmd/seed-demo-data` 向 CRM 本地库写入一组可识别的客户与商机演示数据（幂等，重复执行只跳过已有行，不删除不覆盖）：

- 客户：华兴证券股份有限公司、鹏程能源科技有限公司、滨江医疗集团有限公司、中诚商业银行股份有限公司，含加密联系人、干系人、信息系统与跟进记录；
- 商机：11 条覆盖 初步接触/需求沟通/方案制定/报价/投标/已签约/失败，其中包含 已签约+待关联合同 与 失败+待补原因 两个终态待办样例；
- 负责人/成员使用基础平台 OIDC `sub`（陈浩然、刘明、李婷芳、王建国、赵晓燕），不伪造 PMS `person_id`；
- 数据通过生产服务层与仓储写入：客户号/商机号序列、联系人 AEAD 加密、审计、幂等记录、阶段日志全部走真实链路。

运行要求与 `crm-server` 相同：`MYSQL_DSN`、`SENSITIVE_ENCRYPTION_KEY_BASE64`、`SENSITIVE_HMAC_KEY_BASE64`；可用 `-tenant-id`、`-actor-id`、`-actor-name` 覆盖默认的 `tenant-demo` / `oidc-sub-demo-seed` / `演示数据初始化`。

```bash
MYSQL_DSN='customer:密码@tcp(127.0.0.1:3306)/customer_opportunity?charset=utf8mb4&parseTime=true&loc=UTC' \
SENSITIVE_ENCRYPTION_KEY_BASE64='...' SENSITIVE_HMAC_KEY_BASE64='...' \
go run ./cmd/seed-demo-data
```

本地栈默认租户是 `OIDC_TENANT_ID`（如 `01J00000000000000000000000`），种子命令会自动使用该值；不要把演示数据写到 `tenant-demo`，否则 CRM 会话看不到。

### 门户客户账号开通

```bash
go run ./cmd/seed-portal-invite -customer 华兴证券股份有限公司
```

命令走生产 CM-004 saga：平台预置无密码外部客户账号 → 分配 `customer_portal/portal_customer` → Portal 建立身份映射 → 返回一次性激活链接和登录账号。之后在基础平台“系统设置 → 登录账号”对该账号执行“初始化密码”，再打开激活链接完成登录。该链路本地已验证通过；平台侧曾因 `iam_external_identity.login_account_id` 非空约束与同值 UPDATE 零行影响两个缺陷返回 500/409，已修复于 `platform/internal/platform/externalidentity/infrastructure/gorm_repository.go`。

## 验证命令

```bash
GOCACHE=/tmp/customer-opportunity-gocache go test ./...
GOCACHE=/tmp/customer-opportunity-gocache go vet ./...
go run ./cmd/migration-plan -schema crm -dir migrations
go run ./cmd/migration-plan -schema portal -dir migrations

cd ../frontend
npm test
npm run build
```

当前源码 migration plan 校验结果为 CRM 47 个文件至 `000075`，combined checksum `sha256:994e286df8a5c8152bfff9498d449a089a5ce029cbed47b8314f36f3607afef7`；Portal 30 个文件至 `000077`，combined checksum `sha256:c3f82ff64488ccb9b0218f3594294ae906647d59dd958cd49f454023e9a41299`。该校验只验证文件归属、顺序和 checksum，不等于 MySQL 空库或存量库已执行。2026-08-02 的真实空库/集成证据仍覆盖 CRM 至 `000071`、Portal 至 `000070`；新增迁移仍需补做上一版本增量升级、在线 DDL、metadata lock、回滚和生产规模性能演练。
