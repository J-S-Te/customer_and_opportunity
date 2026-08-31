# Current Task

## 目标

修复信用等级调整全流程审计发现的问题。

## 已完成

- 修复数据库错误被误映射为信用申请 409/审批 404。
- 增加回款事件载荷一致性、来源、日期、金额精度和引用格式校验。
- 增加规则配置基于 `updated_at` 的乐观并发控制。
- Trim 并校验审批驳回意见；撤回接口对非本人申请失败关闭。
- 补充信用模块回归测试，更新前端规则保存请求发送 `updated_at`。

## 验证结果

- `GOCACHE=/tmp/crm-credit-go-cache go test ./...`：通过。
- `npm test -- --runInBand`：428 项通过。
- `npm run build`：通过；存在既有 chunk size warning。

## 外部验证

尚未连接真实 MySQL 验证并发事件重放及通知服务故障策略；需要部署环境凭据和业务方确认后执行。
