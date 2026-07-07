# 客户与商机管理子系统 — API 接口契约（T4）

| 文档信息 | |
|---|---|
| 文档版本 | T4（建议稿） |
| 编制日期 | 2026-07-07 |
| 上游基线 | 《需求说明书 V1.2》《数据字典 V1.0》《ER 图 V1.0》《技术栈建议文档 T1》 |
| 状态 | **建议（待工程确认）** |
| 编制角色 | 产品通 + API 契约设计 |

> **Why**：后端与前端在 M1 启动前需要统一的接口契约，否则会陷入"边写边改"。本文档定义内部 REST API 的通用约定、路由、关键请求/响应结构，字段以数据字典为准。可作为 OpenAPI 3.0 的生成基线。

---

## 一、通用约定

| 项 | 约定 |
|---|---|
| Base URL | `https://{host}/api/v1`（内部后台）；门户独立域 `https://portal.{host}/api/v1` |
| 认证 | 内部：`Authorization: Bearer <JWT>`；门户：独立 JWT（双因子本期站内验证码） |
| 鉴权 | RBAC 角色：销售 / 销售总监 / 财务经理 / 财务总监 / 管理员 / 客户 |
| 内容类型 | `application/json; charset=utf-8` |
| 时间格式 | ISO-8601（`yyyy-MM-dd HH:mm:ss`），时区 GMT+8 |
| 分页 | `?page=1&pageSize=20`；响应 `data={list,total,page,pageSize}` |
| 统一响应体 | 见下 |

**统一响应体**
```json
{ "code": 0, "message": "success", "data": {}, "traceId": "uuid" }
```

**错误码**
| code | 含义 |
|---|---|
| 0 | 成功 |
| 40001 | 参数校验失败 |
| 40101 | 未认证 |
| 40301 | 无权限（角色/数据范围） |
| 40401 | 资源不存在（含软删过滤） |
| 40901 | 业务冲突（如客户查重命中） |
| 42201 | 业务规则拒绝（如审批驳回/阶段非法回退） |
| 50001 | 系统错误 |

> 字段引用：下文各接口字段均对应《数据字典 V1.0》同名表；枚举值以字典全集为准。

---

## 二、客户主数据 API（CM-001/002/003）

| Method | Path | 说明 | 关键入参 | 出参 |
|---|---|---|---|---|
| POST | `/customers` | 新建客户（生成 KH 编号） | 见示例 | `t_customer` |
| POST | `/customers/check-duplicate` | 查重（名称模糊+信用代码精确） | `customerName`,`creditCode` | `{hit:bool, matchedId?}` |
| GET | `/customers` | 分页查询/搜索 | `customerName`,`customerType`,`industry`,`creditLevel`,`status`,`page`,`pageSize` | 列表 |
| GET | `/customers/{customerId}` | 客户详情（含关联子表聚合） | path | 聚合 DTO |
| PUT | `/customers/{customerId}` | 编辑（写操作日志） | 变更字段 | `t_customer` |
| POST | `/customers/{customerId}/merge` | 合并（挂历史，软删） | `targetCustomerId`,`reason` | 新客户 |
| POST | `/customers/{customerId}/credit-adjust` | 信用等级调整（二级审批） | `newLevel`,`reason`,`effectTime` | `t_approval_instance` |
| GET | `/customers/{customerId}/credit-history` | 信用历史 | — | 列表 |
| 子资源 | `/customers/{customerId}/stakeholders` `/systems` `/orgs` `/finance` `/communications` | CRUD 子表 | — | 各子表 |

**示例：POST /customers**
```json
{
  "customerName": "某银行股份有限公司",
  "customerType": "业主",
  "creditCode": "91310000XXXXXXXX0A",
  "industry": "金融",
  "enterpriseScale": "大",
  "regAddress": "上海市浦东新区...",
  "officeAddress": "上海市浦东新区...",
  "biz": {
    "mainBusiness": "商业银行核心系统",
    "testDemandType": ["等保测评","渗透测试"],
    "specialRequirement": "需保密协议"
  },
  "stakeholders": [
    { "name":"张总", "position":"信息化部主任", "phone":"138****0000",
      "decisionWeight":5, "preferenceTags":["技术型"] }
  ],
  "finance": {
    "bankAccount":"**** **** 1234", "accountName":"某银行",
    "bankName":"工行上海分行", "invoiceTitle":"某银行股份有限公司",
    "taxNo":"91310000XXXX", "invoiceAddress":"..."
  }
}
```
> 敏感字段 `phone`/`bankAccount` 由应用层加密存储、按角色脱敏返回（见 T1/V1.3）。

---

## 三、商机跟进 API（BM-001/002/003/004）

| Method | Path | 说明 | 关键入参 |
|---|---|---|---|
| POST | `/opportunities` | 新建商机（生成 SJ 编号） | `customerId`,`oppName`,`oppSource`,`oppType`,`expectedAmount`,`expectedSignDate` |
| GET | `/opportunities` | 看板/列表（按阶段、负责人、状态） | `stage`,`salesOwner`,`status`,`page` |
| GET | `/opportunities/{oppId}` | 商机详情 | — |
| PUT | `/opportunities/{oppId}` | 编辑 | 变更字段 |
| POST | `/opportunities/{oppId}/follow` | 新增跟进记录 | `followTime`,`followType`,`content`,`nextFollowTime`,`reminderChannels` |
| POST | `/opportunities/{oppId}/stage-advance` | 阶段推进（必经销售总监审批） | `toStage`,`keyResult` → 返回审批实例 |
| POST | `/opportunities/{oppId}/lose` | 标记流失 | `lostReason`,`lostType`,`summary` |
| POST | `/opportunities/{oppId}/convert-contract` | 转合同（推送下游，见 T5-A） | — |
| POST | `/opportunities/{oppId}/quotations` | 生成报价单（BJ 编号） | 明细/付款条款 |
| POST | `/quotations/{quotationId}/submit-approval` | 提交报价审批（销售总监+财务经理） | — |
| POST | `/quotations/{quotationId}/convert-contract` | 报价转合同（推下游） | — |
| POST | `/opportunities/{oppId}/bids` | 创建投标项目 | 招标信息 |
| POST | `/bids/{bidId}/win` | 中标（推下游，见 T5-B） | `winningNotice` |
| POST | `/bids/{bidId}/lose` | 落标（申请保证金退还） | `lostReason` |

**示例：POST /opportunities/{oppId}/stage-advance**
```json
{ "toStage":"报价", "keyResult":"已完成方案确认，客户确认预算", "remark":"" }
```
响应：`{ "code":0, "data": { "approvalId":"AP0001", "approvalType":"阶段推进",
"currentStage":"方案制定", "targetStage":"报价", "approveStatus":"待审批" } }`

---

## 四、客户自助门户 API（CP-001/002/003/004、CUS-004）

| Method | Path | 说明 |
|---|---|---|
| POST | `/portal/accounts/open` | 开通门户（生成邀请码/二维码/链接） |
| POST | `/portal/register` | 客户注册（邀请码校验） |
| POST | `/portal/login` | 登录（失败 5 次锁 15 分） |
| GET | `/portal/projects` | 我的项目（拉下游快照，见 T5-D） |
| GET | `/portal/projects/{projectId}/progress` | 项目进度详情 |
| POST | `/portal/reports/apply` | 申请电子报告（推下游审批，见 T5-E） |
| GET | `/portal/reports` | 我的报告申请列表 |
| GET | `/portal/reports/{requestId}/download` | 加密下载（AES-256，见 T5-F） |
| POST | `/portal/feedback` | 提交反馈（超 24h 自动升级） |
| POST | `/portal/evaluations` | 服务评价 |
| POST | `/portal/filings` | 备案信息填写（5 步向导，生成 PDF） |

---

## 五、通用能力 API

| Method | Path | 说明 |
|---|---|---|
| POST | `/attachments/upload` | 多态附件上传（bizType+bizId） |
| POST | `/approvals/{approvalId}/callback` | 审批引擎回调（阶段/报价/信用/作废） |
| GET | `/dict/enums` | 枚举值全集（供前端渲染下拉） |

---

## 六、Non-goals（本期 API 不做）

- 不提供 IM/邮件/短信发送接口（仅站内消息，预留扩展位）。
- 不对外暴露报告文件原始存储路径（仅加密下载链接）。
- 不提供合同草稿生成接口（转合同仅推送数据）。

---

**文档结束**
