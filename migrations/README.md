# 数据库迁移清单

本目录同时保存 CRM 与客户 Portal 两套独立 MySQL 8 schema 的迁移。禁止按文件名通配符把全部 migration 执行到同一个数据库。

## CRM schema

按以下顺序执行：

1. `000001_create_shared.up.sql`
2. `000002_create_customers.up.sql`
3. `000003_create_opportunities.up.sql`
4. `000004_create_portal_invites.up.sql`
5. `000005_presale_requests_approval.up.sql`
6. `000006_presale_assignment_progress.up.sql`
7. `000007_presale_worklog_outbox.up.sql`
8. `000009_crm_followups.up.sql`
9. `000010_customer_change_logs.up.sql`
10. `000012_presale_outbox_lease.up.sql`
11. `000013_crm_oidc_sessions.up.sql`
12. `000014_portal_invite_compensation.up.sql`
13. `000015_crm_opportunity_lifecycle.up.sql`
14. `000019_presale_alerts.up.sql`
15. `000021_presale_reports.up.sql`
16. `000023_opportunity_team.up.sql`
17. `000025_portal_invite_compensation_worker.up.sql`
18. `000026_opportunity_stage_alerts.up.sql`
19. `000027_customer_merge.up.sql`
20. `000029_opportunity_owner_notifications.up.sql`
21. `000030_presale_engineer_sync.up.sql`
22. `000032_customer_query_indexes.up.sql`
23. `000033_customer_profile_children.up.sql`
24. `000034_customer_imports.up.sql`
25. `000035_presale_timeline_indexes.up.sql`
26. `000036_presale_progress_idempotency.up.sql`
27. `000037_presale_query_indexes.up.sql`
28. `000040_presale_mutation_idempotency.up.sql`
29. `000042_presale_assignment_notifications.up.sql`
30. `000044_customer_create_idempotency.up.sql`
31. `000045_opportunity_create_idempotency.up.sql`
32. `000047_opportunity_external_edges.up.sql`
33. `000048_contract_transfer_delivery.up.sql`
34. `000049_opportunity_attachments.up.sql`
35. `000051_presale_progress_notifications.up.sql`
36. `000053_opportunity_member_terms.up.sql`
37. `000055_crm_oidc_session_organizations.up.sql`
38. `000058_crm_oidc_session_person_id.up.sql`
39. `000060_presale_approval_pending_task_binding.up.sql`
40. `000064_portal_invite_operation_idempotency.up.sql`
41. `000065_presale_approval_callback_replay_binding.up.sql`
42. `000067_presale_daily_metrics.up.sql`
43. `000069_crm_portal_access_disable.up.sql`
44. `000071_presale_alert_recipient_namespace.up.sql`
45. `000072_presale_worker_heartbeats.up.sql`
46. `000074_portal_invite_login_account.up.sql`
47. `000075_crm_request_audit_outbox.up.sql`
48. `000078_presale_approval_rules.up.sql`
49. `000080_crm_session_data_scopes.up.sql`
50. `000081_portal_identity_reconciliation.up.sql`
51. `000082_opportunity_contract_link.up.sql`
52. `000083_opportunity_type_source_multiselect.up.sql`
53. `000084_presale_workflow_notifications.up.sql`
54. `000085_presale_approval_notifications.up.sql`
55. `000094_add_user_login_ip_to_request_audit_outbox.up.sql`
56. `000096_add_login_ip_to_crm_oidc_sessions.up.sql`
57. `000098_add_platform_delivered_at_to_crm_notifications.up.sql`
58. `000099_widen_crm_notification_source_event_id.up.sql`

## customer_portal schema

按以下顺序执行：

1. `000008_create_portal_core.up.sql`
2. `000011_portal_session_claims.up.sql`
3. `000016_portal_project_sync.up.sql`
4. `000017_portal_report_delivery.up.sql`
5. `000018_portal_session_revalidation.up.sql`
6. `000020_portal_machine_request_replays.up.sql`
7. `000022_portal_account_security.up.sql`
8. `000024_portal_feedback.up.sql`
9. `000028_portal_service_evaluations.up.sql`
10. `000031_portal_filing.up.sql`
11. `000038_portal_report_status_events.up.sql`
12. `000039_portal_report_download_grants.up.sql`
13. `000041_portal_report_actor_columns.up.sql`
14. `000043_portal_report_issued_notifications.up.sql`
15. `000046_portal_project_exports.up.sql`
16. `000050_portal_project_messages.up.sql`
17. `000052_portal_project_message_read_receipts.up.sql`
18. `000054_portal_project_message_keyset_reads.up.sql`
19. `000056_portal_filing_materials_and_submission_outbox.up.sql`
20. `000057_portal_report_file_security_evidence.up.sql`
21. `000059_portal_report_scan_status_evidence.up.sql`
22. `000061_portal_report_watermark_tracking.up.sql`
23. `000062_portal_filing_waiting_contract_status.up.sql`
24. `000063_portal_report_async_ingest.up.sql`
25. `000066_portal_filing_submission_receipts.up.sql`
26. `000068_portal_report_risk_operations.up.sql`
27. `000070_portal_identity_disable_idempotency.up.sql`
28. `000073_portal_worker_heartbeats.up.sql`
29. `000076_portal_request_audit_outbox.up.sql`
30. `000077_portal_customer_service_options.up.sql`
31. `000079_portal_session_data_scopes.up.sql`
32. `000095_add_user_login_ip_to_portal_request_audit_outbox.up.sql`
33. `000097_add_login_ip_to_portal_sessions.up.sql`

## 规则

- 应使用发布平台维护的 schema migration 版本表记录执行结果；应用进程不使用 GORM `AutoMigrate`。发布器必须串行化同一 schema 的发布，并对每条 MySQL DDL 记录执行前/执行后进度和不可变 checksum；发生中断后只能在验证目标列、索引、约束、表和必要回填均符合预期时收敛，不能因为 duplicate column/table/index/constraint 就笼统视为成功。
- `down.sql` 只用于空环境或已经完成业务数据备份、影响分析和外部关联清理的受控回退；审计、身份映射、终态记录和 outbox 在生产环境优先采用前向修复。
- 新 migration 必须注明目标 schema，并在此清单中登记顺序。
- CRM 与 Portal 可以使用同一个 MySQL 实例，亦可按部署需要使用同一个 database/schema；两套迁移历史仍须独立记录并串行执行，运行账号按各自表收紧权限。两套审计 outbox 使用不同表名和领取租约，禁止一个进程领取另一应用的审计任务，也禁止跨模块业务事务耦合。

`000047` 新建报价/投标可信回调的只读快照表，不从缺少完整来源类型和金额的旧阶段日志伪造历史；`OPPORTUNITY_SIGNED` 转合同接受态复用既有 outbox 和操作人绑定幂等表。

`000049` 新建商机附件对象引用与安全扫描状态机；只保存不可变对象版本、摘要和信任状态，不保存文件二进制。正式对象存储/扫描未配置时业务层在写入会话前失败关闭。
`000053` 新建商机团队独立任期账本。旧成员表只能证明迁移时观察到的当前状态，因此 `LEGACY_SNAPSHOT` 只保存 `snapshot_at` 和 `active_at_snapshot`；`started_at`/`ended_at` 及其操作人必须保持未知。迁移绝不把可反复激活的成员行 `created_at`/`updated_at` 当成连续任期边界，也不从通用审计 JSON 猜测历史。上线后的加入、移出、重新加入和角色变化才以 `RECORDED` 写入完整时间边界，并与当前团队替换在同一事务记录。
`000055` 为 CRM 服务端 OIDC 会话保存基础平台签发的当前直接组织任职快照。存量会话缺少该授权上下文，迁移会补空数组并撤销所有尚未撤销的存量会话，要求用户重新完成 OIDC 登录；不从 CRM 本地业务数据猜测组织关系。
`000058` 为 CRM 服务端 OIDC 会话保存基础平台显式维护并签发的 PMS `person_id`。存量会话在迁移时全部撤销，缺少绑定继续保存空值并使执行人能力失败关闭；绝不从 `sub`、平台用户 ID、账号或员工编号推断 PMS 人员标识。
`000060` 把审批动作时实时解析的权威 task、approver 与 action 原子保存在审批实例；回调只有完全匹配才能推进。历史 pending 实例不回填猜测值，在重新提交动作前保持失败关闭。
`000064` 新建 Portal 邀请预置操作 saga，先于任何外部身份写入持久化 actor/customer/request 绑定的幂等记录；联系人恢复快照与重放 token 只保存认证加密密文，每个外部步骤只按稳定键续跑。旧邀请没有原始调用键和可恢复 token，不做猜测回填。
`000065` 为审批日志追加 engine instance 与 event sequence，使已处理回调的重放也能按实例、任务、序号、节点、审批人和结果完整校验；历史日志保留空/零哨兵，不伪造当时不存在的权威回调证据。
`000059` 为报告文件增加显式扫描结论，旧行默认空且不可下载；只有 `CLEAN` 与对象版本、AES-256-GCM、扫描编号/时间和 SHA-256 证据同时成立才可下载。`000061` 为成功下载事件保存水印追踪码的作用域摘要，不保存明文。`000062` 将旧的本地 `SUBMITTED` 状态前向改名为 `WAITING_CONTRACT`，避免误称公安已提交；未来只有可信回执才能使用 `SUBMITTED`。`000063` 增加报告文件异步 Ingest 作业、加密描述符、稳定事件号、有限租约和重试/死信状态，使对象读取、病毒扫描与信封加密不再发生在 ISSUED 回调事务内；不为存量报告伪造作业。
`000066` 新建不可变公安提交回执表，并增加 `SUBMITTING`/`SUBMISSION_FAILED` 状态。只有正式 Provider 返回经校验的回执后，Worker 才在同一事务写入回执身份、加密证据、明文证据 SHA-256 并把备案改为 `SUBMITTED`；既有 `WAITING_CONTRACT` 行不推断回执、不自动变更状态。
`000067` 新建 TS-009 可重建日报事实表和聚合运行记录。独立 Worker 按 tenant 与 UTC 半开日窗口事务性先删后重建，使用历史人员/部门快照、有效工时和已有 PMS outbox 证据；失败运行不发布部分窗口，事实表不替代不可变业务证据。
`000068` 新建报告风险冻结站内告警和人工复核事件，冻结与告警同事务；人工解冻仅允许恢复尚未过期且不存在其他活动授权的原 grant，撤销重发只撤销旧 grant 并要求客户再次显式生成一次性授权，后台不生成或返回可回放的明文 token。旧 FROZEN grant 缺少可信规则和检测时点，不补造历史告警。
`000069` 新建独立 Portal 访问禁用 Saga。操作绑定身份链接 ID/版本快照，先让 Portal 映射进入 `DISABLED` 并撤销该 subject 的全部本地会话，再回收基础平台 `portal_customer` 角色；两个远端步骤使用稳定业务幂等键。请求与独立恢复 Worker 共享有限租约，到期 `RETRY_WAIT` 由 `FOR UPDATE SKIP LOCKED` 领取，第 8 次失败保留 `DEAD_LETTER` 证据。既有身份链接不自动禁用，邀请 revoke 也不会触发该流程。
`000070` 为 Portal 本地映射禁用增加机器主体+业务幂等键+规范载荷摘要账本；随机 integration nonce 只防单次请求重放，不能替代跨网络重试的业务幂等。映射、会话撤销、最小化 auth event 与账本在同一事务提交，审计失败整体回滚。

`000081` 新建 CRM↔Portal 身份周期对账运行记录与稳定差异记录。对账只读取 CRM identity link、客户、联系人、邀请账号、既有补偿状态以及 Portal 最小身份快照；不会创建第二条补偿任务，也不会自动推断禁用或恢复方向。只有已经处于 `PENDING/PROCESSING/RETRY_WAIT` 的幂等补偿被标记为 `AUTO_COMPENSATION`，其余映射、状态、客户或联系人差异均持久化为 `NEEDS_REVIEW`。生产回退会删除运维证据，存在对账记录时应优先前向修复。

`000073` 为 Portal 报告投递 Worker 与项目 PDF Worker保存多实例运行心跳。Portal 仅在任一对应实例心跳新鲜时受理首次异步工作；已有幂等请求重放不依赖当前心跳。配置存在不等于 Worker 存活，查询错误、心跳过期或无实例均失败关闭。
`000071` 为 TS-008 售前预警增加显式 `USER`/`PERSON` 收件人命名空间，并把命名空间纳入唯一键和个人收件箱索引；`recipient_id` 及该表操作人审计列扩为大小写敏感的 128 字符，以完整承载既有 CRM OIDC `platform_user_id` 契约，PMS `person_id` 仍遵守 64 字符上限。存量 `recipient_id` 缺少可证明来源，统一保留为不可查询的 `LEGACY_UNKNOWN`；只取消尚未投影的旧 PENDING 预警及其 outbox，不把 OIDC `sub` 猜成 PMS `person_id`，也不改写已经投影的历史证据。新增写入必须显式给出命名空间；该迁移仍需上一版本存量数据、在线 DDL 和 metadata lock 演练。
`000085` 扩展 CRM 通知资源形状约束，允许售前审批待处理、通过和驳回通知落库；不为已有待审批记录伪造历史通知，生产环境须执行前向迁移后再启用审批通知写入。
`000101` 仅重新排队历史上因 `source_event_id` 以数字开头而被基础平台编码校验拒绝的通知；新版投递 Worker 会把这类 ID 稳定映射为合法的 `CRM_` 事件编码。其他 422 校验失败不会被重试，避免无效收件人或无效资源形成永久重试循环。
`000072` 新建独立 Worker 多实例心跳表，以 `worker_type + worker_id` 保存最后一次成功读取售前 outbox 的时间。CRM 只在任一 `presale_delivery` 实例具有新鲜数据库证据时接受新的售前申请；无记录、过期或查询失败均失败关闭，已提交申请的幂等重放不受 Worker 暂停影响。首笔心跳与 outbox 查询/领取在同一短事务，写入失败会回滚租约；此后每处理一个具有 5 秒硬超时的外部事件就刷新心跳，刷新失败会告警但不会中断已领取批次。CRM/Worker 使用相同新鲜度窗口，Worker 启动时校验该窗口足以覆盖轮询、外部超时和调度抖动；配置项本身不能把 Worker 标记为可用。
`000048` 新建转合同逐次投递诊断表；既有 `OPPORTUNITY_SIGNED` 仍由 Worker 按原事件 ID 领取，不伪造历史投递成功。
`000046` 新建项目进度 PDF 任务、账号绑定的一次性下载授权和不可变事件表，不从历史项目浏览记录伪造导出请求；生产有导出证据后只允许前向修复。`000016` 的同步游标列使用 `sync_cursor`，避免 MySQL 8.4 保留字 `CURSOR` 导致空库迁移失败。
`000050` 为项目快照增加项目服务权威提供的经理 Portal 接收账号，并新建独立项目会话、纯文本消息和审计事件表；不从姓名、人员引用或脱敏联系方式回填。该迁移已通过 MySQL 8.4 全新 schema 验证；ALTER 在存量表上仍可能取得 metadata lock，上一版本升级和在线 DDL 影响仍待演练。
`000051` 新建 TS-004 过程通知的不可变、显式 user/person 命名空间收件人证据，并扩展 CRM 个人通知投影；不从缺少原始收件人集合与稳定事件身份的历史进度伪造通知。该迁移已纳入 MySQL 8.4.11 当前 CRM 完整空库链并通过外键、CHECK 和通知 shape 验证；上一版本在线升级仍待演练。
`000052` 保存旧客户端项目消息高水位兼容证据；其目标必须属于同一会话且真实投递给当前读取账号，但该高水位语义已由 `000054` 的逐消息回执取代。编号 `000051` 属于独立 CRM schema，两套 schema 必须分别按本清单执行。
`000051/000052/000053` 都包含多个会自动提交的 DDL/DML 语句。发布平台必须按上述语句级进度和结构验证契约执行；在平台尚未具备该能力前，不得把这些文件作为“整文件成功后才记版本”的一次性命令直接发布。`000052` 的回填恢复还必须证明所有 `message_cursor` 均非空且唯一，`000053` 的恢复必须证明 legacy snapshot 不重复且外键/活动任期唯一约束完整。
`000054` 以前向方式停止使用高水位语义，新建租户/会话/账号绑定的逐消息回执，并为消息历史页启用以不透明 `message_cursor` 为锚点的 exclusive keyset 分页。升级只迁入 `000052` 精确指向的目标消息，不推断更早消息已读；旧客户端的 `through_message_cursor` 仅兼容为一条精确消息回执，`page=1` 继续兼容，`page>1` 明确拒绝并要求使用 `before`。
- 合并前必须在全新 MySQL 8 空库执行完整 `up` 链；涉及兼容升级时还需从上一发布版本执行增量验证。

`000040` 还为售前请求增加 `(tenant_id,id)` 复合唯一索引，使幂等协调表可以用复合外键约束父任务租户，并以组合 CHECK 约束 operation/action；父表索引上线须评估 metadata lock。`000041` 仅把报告请求与下载 grant 的 `created_by/updated_by` 扩为 128 字节，以匹配 Portal OIDC subject/account 标识上限；上线仍须验证在线 DDL、metadata lock 与存量索引影响。`000042` 新增 TS 指派不可变事件并扩展 CRM 个人通知投影，不合成存量指派通知。`000043` 新增 Portal 报告发放个人通知和首次已读审计，不为存量 ISSUED 报告伪造通知。`000044/000045` 分别新增客户和商机创建的操作人绑定持久幂等记录；两者都不从缺少原始请求键的存量数据伪造记录，opaque 操作人/键列使用 binary collation。`000024` 也按尚未执行的当前基线把 Portal 反馈外部账号审计列对齐为 128 字节，并对账号和幂等键使用 binary collation；若任何环境已执行旧版 `000024`，禁止重跑，必须另建 forward migration。当前完整空库链已经过 MySQL 8.4.11 验证；上一版本升级、存量数据、在线 DDL 和回退影响仍须单独演练。

2026-08-01 已在 MySQL 8.4 全新数据库重新执行当前完整 `up` 链：CRM 41 个文件（至 `000065`）创建 57 张表，combined checksum 为 `sha256:40384a576aee4ff20cfa786048a85dd37de52828a6ea5bcfe006c8a83583cbad`；Portal 25 个文件（至 `000066`）创建 48 张表，combined checksum 为 `sha256:55e487c054fa7f7617481f95892912a4becd2db05d477b6b9e5102cf9d920664`。已抽查邀请 saga、审批回放列、报告异步 Ingest、备案回执、回执密文字段和解锁取消 outbox 约束，临时容器已删除。合同仓库最近验证至 `000010`，创建 15 张业务表及 2 张 migration metadata 表。编号交错不表示跨 schema 执行。

2026-08-02 已在 MySQL 8.4.11 全新数据库执行 CRM 42 个文件至 `000067`，创建 59 张表，combined checksum 为 `sha256:bff0fd7e919c4816e59509ab9d371d00154d186142347ceb4ff4f9728bf57003`；使用 1 条有效工时与 PMS outbox 证据连续运行聚合两次，事实表仍为 1 行、2.00 小时、PMS 分母/成功数均为 1，两次运行记录均为 `SUCCESS`，临时 schema 已删除。该验证不等于大数据量性能、存量升级或在线 DDL 验收。

2026-08-06 当前源码 migration plan 检查：CRM 47 个文件至 `000075`，combined checksum 为 `sha256:994e286df8a5c8152bfff9498d449a089a5ce029cbed47b8314f36f3607afef7`；Portal 30 个文件至 `000077`，combined checksum 为 `sha256:c3f82ff64488ccb9b0218f3594294ae906647d59dd958cd49f454023e9a41299`。该命令只验证文件归属、顺序和 checksum，不代表数据库已执行；真实空库、存量增量、在线 DDL、metadata lock、回滚和生产性能仍需单独留存证据。

2026-08-02 已在 MySQL 8.4 全新数据库按发布清单执行 CRM 44 个文件至 `000071`，combined checksum 为 `sha256:047aea0f5a1664b98982d1884873f8ef7afc773caed644d694f39455f0b5d01c`。已实际核查 `recipient_kind` 为无默认值的 NOT NULL 列、`recipient_id/created_by/updated_by` 为大小写敏感的 128 字符、去重键包含 `recipient_kind`、个人查询索引顺序为 `tenant_id,recipient_kind,recipient_id,status,created_at`，且 CHECK 仅允许 `USER/PERSON/LEGACY_UNKNOWN`；临时容器已删除。该空库验证不替代 `000071` 的存量 PENDING 取消结果、在线 DDL、metadata lock 或回退唯一键冲突演练。

2026-08-02 已在 MySQL 8.4 全新数据库按发布清单执行 Portal 27 个文件至 `000070`，combined checksum 为 `sha256:b9b8b281741b4ec28662d04c2ffab82e850f6fb849709b914807262236637ae3`。另以真实 MySQL 事务集成测试验证禁用映射、全部 subject 会话撤销、业务幂等与最小化审计原子提交，审计写入失败时所有状态和账本整体回滚；临时容器已删除。该验证不替代存量升级、在线 DDL、正式机器 Client 和生产并发/UAT。

2026-08-02 已在 MySQL 8.4.11 全新数据库执行 Portal 26 个文件至 `000068`，创建 50 张表，combined checksum 为 `sha256:4c48f596aa91b63408725507eebb63c069bb7b026d3af4e3ede399c62689b1c0`；已实际抽查风险告警/复核表的租户+客户+报告+grant 复合外键、单 grant 单 OPEN 告警、操作人幂等复核键及状态/恢复动作 CHECK，临时 schema 已删除。该结果仍不替代 `000068` 在存量 grant 表新增复合唯一索引时的在线 DDL 与 metadata lock 演练。

`000053` 的快照时间语义修正后，又使用“可复用成员行且原始 created_at 远早于当前状态”的活动/停用样例在 MySQL 8.4.11 执行。结果确认两类 `LEGACY_SNAPSHOT` 的 `started_at`/`ended_at`/`started_by` 均不被伪造，只有迁移时活动的快照占用活动任期唯一键；该定向验证 schema 已删除。

该结果证明当前空库迁移链可执行，不等于上一版本升级、在线 DDL 或生产性能验收完成。`000032/000033/000035/000036/000037/000040/000041/000045/000050` 等在存量表上增加索引、约束、列或改变列长度的迁移仍须使用发布平台认可的在线变更方案，并监控 metadata lock、副本延迟、临时磁盘及回退条件。相应 `down.sql` 会删除不可变协调或审计证据、索引或缩窄字段，只能用于确认数据满足回退条件的测试/空环境，不能作为已有生产事件时的常规回滚。
