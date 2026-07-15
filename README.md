# 客户与商机管理子系统 - 前端工程

基于 Vue 3 + Vite + Element Plus 实现，对应需求说明书 V1.2 / 数据字典 V1.0 / API 接口文档 V1.0。

## 功能特性

- 管理端：客户管理 / 商机看板 / 报价管理 / 投标管理
- 客户门户端：项目进度查询 / 报告申请 / 备案信息 / 服务评价 / 反馈
- 完整 14 页面 + 4 大表单（客户、商机、报价、投标）
- 一体化 Mock 数据中心：覆盖 23 张业务表，无需后端即可演示
- 看板拖拽 / 多步表单向导 / 星级评分 / 审批流展示

## 技术栈

Vue 3.4 + Vite 5 + Vue Router 4 + Pinia 2 + Element Plus 2.6 + Axios 1.6 + Day.js + SCSS

## 快速开始

环境要求：Node.js >= 18.0

```bash
cd frontend
npm install
npm run dev          # 启动开发服务器 (http://localhost:8080)
npm run build        # 构建生产包 (输出到 dist/)
```

## 测试账号

| 模式 | 账号 | 密码 |
|---|---|---|
| 管理端 | 任意 | 任意 |
| 门户端 | huaxing | demo123（任意 6 位验证码） |

## 环境变量

复制 .env.example 为 .env.local：

```bash
VITE_USE_MOCK=true   # 是否启用 mock
VITE_API_BASE=/api   # 后端 API 基础地址
```

## 切换到真实后端

1. 设置 VITE_USE_MOCK=false
2. 在 vite.config.js 配置 server.proxy
3. 对接 API 接口文档 V1.0 即可

## 页面路由

### 管理端
- /admin/login - 登录
- /admin/customers - 客户列表
- /admin/customers/new - 新建客户
- /admin/customers/:id - 客户详情
- /admin/opportunities - 商机看板（拖拽）
- /admin/opportunities/new - 新建商机
- /admin/opportunities/:id - 商机详情
- /admin/quotations - 报价管理
- /admin/quotations/new - 新建报价
- /admin/quotations/:id - 报价详情
- /admin/bids - 投标管理
- /admin/bids/new - 新建投标
- /admin/bids/:id - 投标详情

### 门户端
- /portal/login - 客户登录
- /portal/dashboard - 工作台
- /portal/projects - 我的项目
- /portal/reports - 报告中心
- /portal/filing - 备案信息（5 步向导）
- /portal/evaluation - 服务评价
- /portal/feedback - 反馈与投诉

## 已实现需求点

### 客户主数据 (CM-001 / CM-002 / CM-003)
客户录入与维护、查重、合并、信用代码校验；客户列表 + 多维筛选 + 风险客户高亮；客户详情（基本信息/干系人/商机/系统/操作日志）；客户状态机：潜在/在跟/成交/流失/作废。

### 商机跟进 (BM-001 / BM-002)
商机创建、5 阶段看板拖拽；商机详情（阶段进度条 + 跟进时间轴 + 团队）；阶段推进触发销售总监审批；商机转合同（推送下游）。

### 报价管理 (BM-003)
结构化付款条款（默认 10 工作日 50% + 验收 50%）；自定义条款 + 比例合计校验；审批流（销售总监 → 财务经理）；报价转合同。

### 投标管理 (BM-004)
标书/保证金管理；开标记录、中标落标处理；中标信息推送下游。

### 客户门户 (CP-001 ~ CP-004 / CUS-004)
门户登录 + 双因素认证；项目进度查询；电子报告申请（AES-256 加密提示）；备案信息 5 步向导；服务评价（4 维度星级评分）；反馈与投诉（24h 响应承诺）。

## 文档对照

- 需求：客户与商机管理子系统需求说明书 V1.2
- 数据模型：客户与商机管理子系统数据字典 V1.0
- API：API 接口文档 V1.0
