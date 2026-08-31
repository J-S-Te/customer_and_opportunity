# Project Context

## 项目简介

客户与商机管理 CRM 单体服务，使用 Go、Gin、GORM 和 MySQL；前端位于同级工作区的 `../frontend`，使用 Vue/Vite。

## 关键链路

信用等级由 CRM 内部处理回款事件、自动评估、人工申请、销售总监审批、历史记录和通知；所有业务数据按租户隔离，客户可见性还受角色/数据范围约束。

## 验证方式

后端使用 `go test ./...`，前端使用 `npm test` 和 `npm run build`。本机 Go 默认缓存目录可能无权限，必要时设置 `GOCACHE` 到可写临时目录。
