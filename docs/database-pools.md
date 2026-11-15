# 长期进程连接池预算

CRM API、独立客户门户与长期Worker使用`internal/shared/databasepool`装配池；
原MySQL方言、GORM事务/错误配置和启动Ping策略由调用方保留。
一次性seed工具和测试数据库辅助函数不属于长期运行入口。

| 进程 | 环境前缀 | 最大连接 | 最大空闲 |
| --- | --- | --- | --- |
| CRM API | `CRM_DB_` | 30 | 10 |
| Portal API | `PORTAL_DB_` | 20 | 5 |
| 每个独立Worker | `WORKER_DB_` | 5 | 2 |

所有前缀均支持以下四个后缀：`MAX_OPEN_CONNS`、`MAX_IDLE_CONNS`、
`CONN_MAX_LIFETIME`、`CONN_MAX_IDLE_TIME`。生命周期默认`15m`，空闲期限默认`2m`。
留空时使用默认值；最大连接必须大于零，最大空闲允许零但不能超过最大连接；
两个期限都须是大于零的Go duration。非法配置在打开GORM句柄前返回启动错误。
设置不会把原本允许依赖暂不可用的CRM API改成强制Ping，Portal/既有Worker的探测仍保留。

预算按进程和副本累加，多个Worker各有独立池。调整时同时考虑数据库连接上限、
其他服务和运维余量。这里只提供源码与配置示例；运行编排透传和部署变更需另行授权。
