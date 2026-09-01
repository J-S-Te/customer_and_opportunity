# Current Task

## 目标

对客户与商机管理系统进行业务层深度测试，并修复发现的问题。

## 已完成

- 修复数据库错误被误映射为信用申请 409/审批 404。
- 增加回款事件载荷一致性、来源、日期、金额精度和引用格式校验。
- 增加规则配置基于 `updated_at` 的乐观并发控制。
- Trim 并校验审批驳回意见；撤回接口对非本人申请失败关闭。
- 补充信用模块回归测试，更新前端规则保存请求发送 `updated_at`。

## 本轮深测修复

- 服务层拒绝客户名称、客户基础字段、商机名称/类型/来源/需求说明中的纯空白值。
- 联系人姓名和电话拒绝纯空白值。
- 客户作废/恢复、商机阶段变更原因统一 Trim 并拒绝空白审计原因。
- 增加客户和商机主数据边界回归测试。

## 验证结果

- `GOCACHE=/tmp/crm-credit-go-cache go test ./...`：通过。
- `npm test -- --runInBand`：428 项通过。
- `npm run build`：通过；存在既有 chunk size warning。
- `GOCACHE=/tmp/crm-deep-go-cache go vet ./...`：通过。

## 外部验证

尚未连接真实 MySQL 验证并发事件重放、锁竞争及通知服务故障策略；需要部署环境凭据和业务方确认后执行。

## 信用审批待办修复（2026-09-01）

### 目标

修复销售总监在信用审批待办中无法点击“通过/驳回”的问题，并保证后端审批接口同步放行。

### 已完成

- 对信用审批待办查询、通过、驳回接口增加受控的 `sales_director` / `crm_super_admin` 角色兼容校验，兼容已签发但缺少新目录权限的旧会话。
- 保留后端最终权限、租户、状态、版本和客户信用等级校验；没有把角色全局转换为权限。
- 补齐 `crm_super_admin` 的 `customer.credit.approve` 正式目录权限。
- 前端入口和审批按钮同步识别上述角色，并将角色传入审批组件。
- 驳回意见前端校验与后端最少两个字符规则一致。

### 验证结果

- `env GOCACHE=/tmp/crm-credit-go-cache go test ./...`：通过。
- `env GOCACHE=/tmp/crm-credit-go-cache go vet ./...`：通过。
- `npm test`：430 项通过。
- `npm run build`：通过；存在既有 chunk size warning。
