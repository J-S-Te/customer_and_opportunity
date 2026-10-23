# CRM / Customer Portal commercial runtime

CRM and Customer Portal are separate licensed applications even though they share
this repository. Each API and Worker requires an independent managed runtime
Service ID, OAuth Client/Secret (`license.runtime` only), and persisted state file.
Do not reuse audit, catalog publisher or interactive-login credentials.

`COMMERCIAL_LICENSE_ENABLED=false` is the compatibility default before controlled
enrollment. Set true only via the reviewed platform process. Invalid trust/binding
or incomplete credential configuration refuses startup; absent or invalid signed
state refuses business operations. A platform outage never extends license expiry.

Common `COMMERCIAL_LICENSE_` variables: INSTANCE_ID, ENVIRONMENT, SERVICE_ID,
STATE_PATH, PLATFORM_PUBLIC_KEY_PATH, PLATFORM_BASE_URL, CLIENT_ID, CLIENT_SECRET,
COVERAGE_DIGEST, IMAGE_DIGEST, ALLOW_HTTP. Digests are SHA-256 with `sha256:` prefix.
PLATFORM_PUBLIC_KEY_PATH is read-only Ed25519 public-key PEM. The real vendor key
is embedded in the reviewed consumer; deployment cannot replace it. ENVIRONMENT
uses the commercial installation binding, not OAuth's `prod`/`dev` alias. HTTP
requires controlled explicit ALLOW_HTTP=true. Credentials and state directories
must be per component; secrets must not enter source control or logs.

API policy uses a concrete reviewed route map, not the HTTP method as a license
policy. Unknown routes are business mutations. Existing historical queries and
customer/report/project export policy remains allowed after expiry, with unchanged
identity, permission and data-scope checks. Portal GET /activate is a business
operation, whereas login/logout, account safety revocation, personal read markers
and CRM invite verification remain operational. Invite consumption and new account
provisioning do not inherit that exemption.

`internal/commercial.Start(ctx, application)` installs the real shared consumer.
API shutdown cancels synchronization. Worker integration must check each task at
its execution and persistence boundary, not just during startup; already generated
notification delivery and historical export must not be classified as new business.

Independent CI/Docker consume only `third_party/license-core` reviewed production
sources and check their file allowlist and SHA-256 manifest. Sync with
`sh scripts/license-core-sync.sh --sync ../license-core` only when intentionally
updating that shared source, then review both source and manifest. No sibling
workspace is required for production builds.

Code integration does not prove enrollment or deployment. Platform must deliver
each component's binding and verify ready/snapshot/ACK before ENFORCED. Current
tests use isolated in-memory fixtures; they do not write the CRM business database.

## 操作步骤、导出限制与验收（2026-10-13）

CRM和门户独立售卖。管理员通过受控流程下发每个选中组件的独立身份，许可按实际批准集合核对，不能固定写CRM8/门户2。当前生产runtime_services为CRM API加8个Worker，Portal为API/report-worker/invite-compensation-worker；env_file消费者用于重载，不等于许可批准集合。成功须部署READY、ENFORCED、snapshot_current=true与全部选中成员当期ACK，API确认不能代替其他成员。

到期立即限制新客户、导入预览/提交、商机推进、售前业务、门户邀请消费/新账号及新报告申请，没有写入宽限。既有授权项保留且生效、可信快照及原权限正常才有历史读/导出；客户导出是真实同步CSV，frontend customer_opportunity/api/customer.js → POST customer-exports → routes权限/前缀及商业EXPORT_HISTORY → customer.Service.CreateExport分页List/范围和负责人解析 → 临时CSV响应后清理，不依赖导出Worker。

分类允许EXPORT_HISTORY不制造功能：售前报表RequestExport仍CRM_PRESALE_REPORT_EXPORT_UNAVAILABLE；备案PDF已有真实渲染器，需PDF配置/字体、权限、版本及加密审计，缺依赖PORTAL_FILING_EXPORT_UNAVAILABLE。已有报告下载和新申请不同；项目PDF需独立Worker/字体/任务快照及真实文件，不能以任务号代替。当前生产清单未启动的Worker需另走受控接入，不据许可分类宣称已运行。

失效邀请验证/审计、账号安全撤销、个人已读及既有通知投递保留；邀请consume/provision、角色映射、新owner/派工/进度通知投影仍受业务门禁。失败区分业务权限、缺授权/到期、未实现导出、Worker/文件依赖及数据范围；修真实配置后重试，不停所有Worker或伪造成功。

依据：internal/bootstrap/app.go/portalbootstrap装配 → commercial/gate.go和routes.go → modules/customer handler/service/repository、presale/report.go、portalbootstrap/router.go → workerlicense、projectexportworker/worker.go、presaleworkflow及portalinvitecompensationworker任务复核 → 数据/队列。当前前端商业授权未提交改动只隐藏未购买许可卡片，不改变CRM售卖或权限。此轮未执行导出、Worker或业务库验收。
