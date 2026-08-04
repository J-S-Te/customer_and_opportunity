# 客户与商机管理测试数据脚本

这组脚本为本地 docker 环境（`platform/compose.local.yaml`）准备覆盖各功能模块的测试数据，
**不创建用户、角色、OIDC 会话或登录账号**，只复用已有演示人员/门户身份映射。

## 用法

```bash
bash scripts/testdata/import-testdata.sh
```

默认容器名：

- `basic-platform-local-customer-mysql-1`（customer_opportunity）
- `basic-platform-local-portal-mysql-1`（customer_portal）

如果容器名不同，用环境变量覆盖：

```bash
CUSTOMER_MYSQL_CONTAINER=xxx PORTAL_MYSQL_CONTAINER=yyy bash scripts/testdata/import-testdata.sh
```

密码从对应 API 容器环境变量自动读取，脚本不写死任何秘密。

## 推荐：JSON 请求体 + Docker 内执行（不碰正式容器）

如果你希望用 JSON 数据直接通过应用 HTTP API 写入，而不是直接写表，使用：

```bash
# 服务器（CI/CD 部署目录）
DEPLOY_PATH=/opt/basic-platform bash scripts/testdata/run-json-in-docker.sh

# 本地
bash scripts/testdata/run-json-in-docker.sh
```

执行过程：

1. 从已运行的 `customer-api` 容器复制环境，启动一个**临时的** `DEV_AUTH_ENABLED=true`
   CRM 容器（`crm-json-seed`），连接同一个 MySQL、挂载同一 JWT 公钥；
2. 在临时容器内用 `curl` + `X-Dev-*` 请求头调用真实 HTTP API；
3. 自动创建客户 → 商机 → 商机跟进 → 阶段流转 → 售前申请，并用返回的 `data.id`
   串联后续请求；
4. 结束后自动删除临时容器，正式业务容器和部署配置完全不变。

JSON 文件在 `scripts/testdata/json/`：

- `customer-01.json` / `customer-02.json` / `customer-03.json`
- `opportunity-01.json` / `opportunity-02.json` / `opportunity-03.json`
- `followup-opportunity-01.json`
- `stage-change-01.json`
- `presale-01.json`

说明：

- 售前申请依赖 presale-worker 的实时心跳；如果服务器还没部署/启动 presale-worker，
  该步骤会返回 `INTEGRATION_DEPENDENCY_UNAVAILABLE`，脚本会继续并提示，不中断其余导入。
- 重复执行时，客户按统一社会信用代码复用已有记录；商机会继续追加（可用
  `CRM_TEST_KEY_PREFIX` 换一组幂等键，避免历史幂等记录冲突）。
- 需要覆盖门户（项目/报告/反馈/备案）或预警/通知等 worker 生成数据时，仍请配合
  `04_portal.sql` 等 SQL 文件，或先确保对应项目快照/Worker 已就绪。

## 文件与覆盖范围

| 文件 | 库 | 覆盖功能 |
| --- | --- | --- |
| `01_crm_core.sql` | customer_opportunity | 客户主数据、联系人、干系人、系统备案、客户跟进；商机、阶段、团队、商机跟进；附件、外部报价/投标、合同转交、客户导入、合并、审计、变更日志 |
| `02_crm_presale.sql` | customer_opportunity | 售前工程师、申请（待审批/已批准/执行中/已完成/已拒绝）、审批实例与日志、指派、进展、工时、预警、每日指标、站内通知 |
| `03_crm_alerts_portal.sql` | customer_opportunity | 商机阶段预警规则与实例、负责人/指派/进展通知、Outbox、Portal 邀请与身份映射 |
| `04_portal.sql` | customer_portal | 项目快照、里程碑、活动、项目团队；项目沟通（会话/消息/已读）；项目导出；报告申请/文件/授权/通知/状态/风险；服务评价；客户反馈；备案表/章节/矩阵/材料 |

## 注意事项

- 脚本使用固定 ID 与业务编号（`KH20260804TEST*`、`SJ20260804TEST*`、`TS20260804TEST*`、`FIL-TEST-*` 等）。
  重复导入会因主键/唯一键冲突失败；重跑前请先删除测试 ID 段的数据，或自行改成 `INSERT ... ON DUPLICATE KEY UPDATE`。
- 联系方式的手机/邮箱密文使用当前 docker 环境的 `SENSITIVE_ENCRYPTION_KEY_BASE64` / `SENSITIVE_HMAC_KEY_BASE64` 生成。
  如果更换过密钥，需要用 `cmd/seed-demo-data` 同款 codec 重新生成密文，或改用应用层种子命令导入。
- 附件、报告文件、备案材料只写对象引用元数据，不包含真实文件内容；对象存储/病毒扫描为本地模拟值。
- 脚本不会触碰 `crm_oidc_sessions`、`portal_sessions`、角色/权限目录等用户与角色数据。

## 验证

每个 SQL 文件都可在事务内回滚验证（不落库）：

```bash
{ printf 'START TRANSACTION;\n'; cat scripts/testdata/01_crm_core.sql; printf '\nROLLBACK;\n'; } |
  docker exec -i basic-platform-local-customer-mysql-1 sh -c 'MYSQL_PWD=<密码> mysql ...'
```
