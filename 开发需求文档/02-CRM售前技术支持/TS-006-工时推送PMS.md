# TS-006 工时推送 PMS

## 1. 目标

把 TS-005 已提交工时可靠推送到项目管理系统时间计划。标准单位固定为小时；PMS 失败不影响 CRM 工时和任务完成。

## 2. 事件契约

Topic：`crm.presale.worklog.created`；投递语义至少一次；幂等键必须唯一到 worklog。旧 PRD 的 `taskId+personId+workDate` 无法支持同日多笔，正式采用 `worklogId`，同时保留业务字段供 PMS 追溯。

```json
{
  "eventType": "PRESALE_WORKLOG_CREATED",
  "eventVersion": 1,
  "worklogId": "WL202607310001",
  "taskId": "TS202607310001",
  "opportunityId": "SJ202607070001",
  "personId": "PMS-U10086",
  "implementerDept": "安全技术部",
  "personName": "王建国",
  "workStartTime": "2026-07-31T01:00:00Z",
  "workEndTime": "2026-07-31T09:00:00Z",
  "unit": "小时",
  "workHours": "8.00",
  "rawUnit": "人天",
  "rawValue": "1.00",
  "conversionFactor": "8.00",
  "workSiteAddress": "客户现场",
  "venue": "现场",
  "workContent": "方案设计",
  "idempotencyKey": "WL202607310001",
  "occurredAt": "2026-07-31T09:05:00Z"
}
```

## 3. 推送状态

`PENDING → SENDING → SUCCESS`；失败后 `RETRY_WAIT`，最多 6 次后 `DEAD_LETTER`。人工重试将 DEAD_LETTER/RETRY_WAIT 转 PENDING，不新建业务工时。

重试建议：1m、5m、15m、1h、3h、6h，加随机抖动。错误摘要脱敏保存，响应正文限长。

## 4. 后端

Outbox Worker 发布 MQ；若采用 HTTP，则机器 Client 仅具 `pms.worklog.write` scope，连接超时 2s、总超时 5s。更新 `crm_presale_worklogs.push_status` 和 `crm_integration_attempts`。

投递状态和补偿由内部 Worker、审计记录及受控运维流程处理；历史 `POST /presale/worklogs/{id}/retry` 与 `GET /presale/worklogs/{id}/delivery` HTTP 接口已废弃并移除，前端不再暴露手动重试或投递详情入口。

## 5. 前端与运维

工时列表显示成功、发送中、待重试、失败；有权限用户可重试。监控：成功率、延迟、积压、重试数、死信数；死信 >0 立即告警。

## 6. 验收

- payload 字段、单位和精度符合契约；同一 worklog 重投 PMS 只计一次。
- 失败按策略重试并最终进入死信，可人工恢复。
- 任何推送失败都不回滚 worklog 或 COMPLETED 任务。
- 日志不含 Token/Secret/完整敏感联系人信息。
- 99% 工时在正常依赖下于约定 SLA 内成功送达。
