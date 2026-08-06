-- 售前技术支持（TS）测试数据
-- 适用库：customer_opportunity（本地 docker：basic-platform-local-customer-mysql-1）
SET NAMES utf8mb4;
SET @tenant = '01J00000000000000000000000';
SET @zero_hash = '0000000000000000000000000000000000000000000000000000000000000000';
SET @actor = '01KYDVHC00000000000000000C';
SET @actor_name = '张伟';
SET @now = NOW(3);
SET @phone_cipher = X'B427F1E769B6939A9B654747FA483AF4A4C1967920D0F526E42985D5D7F6F54689BC33FDBAE2FB';

-- 售前工程师池（引用演示人员，不创建用户/角色）
INSERT INTO crm_presale_engineers
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   person_id, person_name, department, role, skill_tags_json, contact_cipher, valid_flag, source_updated_at, synced_at)
VALUES
  (5001, @tenant, @actor, @actor, @now, @now, NULL, 1, '01KYDVHC00000000000000000C', '张伟', '华东一区', 'ENGINEER', JSON_ARRAY('等保','渗透测试'), @phone_cipher, 1, @now, @now),
  (5002, @tenant, @actor, @actor, @now, @now, NULL, 1, '01KYDVHC00000000000000000F', '陈晨', '华东一区', 'ENGINEER', JSON_ARRAY('安全运营'), @phone_cipher, 1, @now, @now),
  (5003, @tenant, @actor, @actor, @now, @now, NULL, 1, '01KYDVHC00000000000000000G', '刘洋', '华南一区', 'ENGINEER', JSON_ARRAY('等保','代码审计'), @phone_cipher, 1, @now, @now),
  (5004, @tenant, @actor, @actor, @now, @now, NULL, 1, '01KYDVHC00000000000000000E', '王强', '华南一区', 'ENGINEER', JSON_ARRAY('风险评估'), @phone_cipher, 1, @now, @now),
  (5005, @tenant, @actor, @actor, @now, @now, NULL, 1, '01KYDVHC00000000000000000D', '李娜', '华东一区', 'ENGINEER', JSON_ARRAY('安全建设'), @phone_cipher, 1, @now, @now);

-- 售前预警规则
INSERT INTO crm_presale_alert_rules
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version, type, threshold_hours, enabled, config_version)
VALUES
  (5101, @tenant, @actor, @actor, @now, @now, NULL, 1, 'APPROVAL_NODE_1_OVERDUE', 24, 1, 1),
  (5102, @tenant, @actor, @actor, @now, @now, NULL, 1, 'ASSIGNMENT_OVERDUE', 48, 1, 1),
  (5103, @tenant, @actor, @actor, @now, @now, NULL, 1, 'EXECUTION_OVERDUE', 72, 1, 1);

INSERT INTO crm_presale_alert_rule_versions
  (id, tenant_id, rule_id, type, threshold_hours, enabled, config_version, changed_by, request_id, changed_at)
VALUES
  (5201, @tenant, 5101, 'APPROVAL_NODE_1_OVERDUE', 24, 1, 1, @actor, 'req-alert-rule-1', @now),
  (5202, @tenant, 5102, 'ASSIGNMENT_OVERDUE', 48, 1, 1, @actor, 'req-alert-rule-2', @now),
  (5203, @tenant, 5103, 'EXECUTION_OVERDUE', 72, 1, 1, @actor, 'req-alert-rule-3', @now);

-- 售前申请：覆盖待审批、已批准待分派、执行中、已完成、已拒绝
INSERT INTO crm_presale_requests
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   request_no, opportunity_id, opportunity_no_snapshot, applicant_id, applicant_name_snapshot,
   venue, service_address, contact_name, contact_phone_cipher, contact_phone_masked, description,
   expected_start, expected_end, urgency, status, current_approval_node, reject_reason,
   completed_at, cancelled_at, create_idempotency_key, create_request_hash)
VALUES
  (6001, @tenant, '01KYDVHC00000000000000000C', '01KYDVHC00000000000000000C', @now, @now, NULL, 1,
   'TS20260804TEST001', 3001, 'SJ20260804TEST001', '01KYDVHC00000000000000000C', '张伟',
   'ONSITE', '广州市天河区科韵路 100 号', '周正', @phone_cipher, '138****6789',
   '工业互联网云平台等保测评现场支持。', '2026-08-10 09:00:00.000', '2026-08-14 18:00:00.000', 'NORMAL',
   'PENDING_APPROVAL', 1, '', NULL, NULL, 'seed-presale-6001', @zero_hash),
  (6002, @tenant, '01KYDVHC00000000000000000D', '01KYDVHC00000000000000000D', @now, @now, NULL, 1,
   'TS20260804TEST002', 3002, 'SJ20260804TEST002', '01KYDVHC00000000000000000D', '李娜',
   'REMOTE', '', '孙立', @phone_cipher, '138****6890',
   '医院信息系统测评远程支持。', '2026-08-12 09:00:00.000', '2026-08-16 18:00:00.000', 'URGENT',
   'APPROVED_PENDING_ASSIGNMENT', 2, '', NULL, NULL, 'seed-presale-6002', @zero_hash),
  (6003, @tenant, '01KYDVHC00000000000000000F', '01KYDVHC00000000000000000F', @now, @now, NULL, 1,
   'TS20260804TEST003', 3003, 'SJ20260804TEST003', '01KYDVHC00000000000000000F', '陈晨',
   'ONSITE', '武汉市东湖高新区高新大道 1 号', '何平', @phone_cipher, '138****6789',
   'ERP/MES 安全加固现场实施。', '2026-08-05 09:00:00.000', '2026-08-20 18:00:00.000', 'NORMAL',
   'EXECUTING', 2, '', NULL, NULL, 'seed-presale-6003', @zero_hash),
  (6004, @tenant, '01KYDVHC00000000000000000G', '01KYDVHC00000000000000000G', @now, @now, NULL, 1,
   'TS20260804TEST004', 3004, 'SJ20260804TEST004', '01KYDVHC00000000000000000G', '刘洋',
   'REMOTE', '', '郑敏', @phone_cipher, '139****6990',
   '数据中台安全咨询交付。', '2026-08-01 09:00:00.000', '2026-08-04 18:00:00.000', 'NORMAL',
   'COMPLETED', 2, '', '2026-08-04 17:30:00.000', NULL, 'seed-presale-6004', @zero_hash),
  (6005, @tenant, '01KYDVHC00000000000000000D', '01KYDVHC00000000000000000D', @now, @now, NULL, 1,
   'TS20260804TEST005', 3007, 'SJ20260804TEST007', '01KYDVHC00000000000000000D', '李娜',
   'ONSITE', '上海市浦东新区民生路 1 号', '冯远', @phone_cipher, '138****6890',
   'TMS 系统安全评估现场勘查。', '2026-08-02 09:00:00.000', '2026-08-03 18:00:00.000', 'NORMAL',
   'REJECTED', 2, '客户预算不足，暂缓执行', NULL, NULL, 'seed-presale-6005', @zero_hash);

-- 审批实例
INSERT INTO crm_presale_approval_instances
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   request_id, engine_instance_id, status, current_node, last_event_seq, pending_task_id, pending_approver,
   pending_action, started_at, finished_at)
VALUES
  (6101, @tenant, @actor, @actor, @now, @now, NULL, 1, 6001, 'engine-inst-6001', 'RUNNING', 1, 0, 'task-6001-1', '01KYDVHC00000000000000000E', 'PASS', '2026-08-04 10:00:00.000', NULL),
  (6102, @tenant, @actor, @actor, @now, @now, NULL, 1, 6002, 'engine-inst-6002', 'RUNNING', 2, 1, 'task-6002-2', '01KYDVHC00000000000000000C', 'PASS', '2026-08-04 10:05:00.000', NULL),
  (6103, @tenant, @actor, @actor, @now, @now, NULL, 1, 6003, 'engine-inst-6003', 'FINISHED', 2, 2, '', '', '', '2026-08-03 09:00:00.000', '2026-08-03 09:30:00.000'),
  (6104, @tenant, @actor, @actor, @now, @now, NULL, 1, 6004, 'engine-inst-6004', 'FINISHED', 2, 2, '', '', '', '2026-07-31 09:00:00.000', '2026-07-31 09:30:00.000'),
  (6105, @tenant, @actor, @actor, @now, @now, NULL, 1, 6005, 'engine-inst-6005', 'FINISHED', 1, 1, '', '', '', '2026-08-01 09:00:00.000', '2026-08-01 10:00:00.000');

-- 审批日志
INSERT INTO crm_presale_approval_logs
  (id, tenant_id, request_id, node, approver_id, approver_name_snapshot, result, comment, approved_at,
   engine_task_id, engine_instance_id, event_sequence, request_id_trace)
VALUES
  (6201, @tenant, 6002, 1, '01KYDVHC00000000000000000E', '王强', 'PASS', '同意', '2026-08-04 10:10:00.000', 'task-6002-1', 'engine-inst-6002', 1, 'req-trace-6002'),
  (6202, @tenant, 6003, 1, '01KYDVHC00000000000000000E', '王强', 'PASS', '同意', '2026-08-03 09:05:00.000', 'task-6003-1', 'engine-inst-6003', 1, 'req-trace-6003'),
  (6203, @tenant, 6003, 2, '01KYDVHC00000000000000000C', '张伟', 'PASS', '同意', '2026-08-03 09:30:00.000', 'task-6003-2', 'engine-inst-6003', 2, 'req-trace-6003'),
  (6204, @tenant, 6004, 1, '01KYDVHC00000000000000000E', '王强', 'PASS', '同意', '2026-07-31 09:05:00.000', 'task-6004-1', 'engine-inst-6004', 1, 'req-trace-6004'),
  (6205, @tenant, 6004, 2, '01KYDVHC00000000000000000C', '张伟', 'PASS', '同意', '2026-07-31 09:30:00.000', 'task-6004-2', 'engine-inst-6004', 2, 'req-trace-6004'),
  (6206, @tenant, 6005, 1, '01KYDVHC00000000000000000E', '王强', 'REJECT', '预算不足', '2026-08-01 10:00:00.000', 'task-6005-1', 'engine-inst-6005', 1, 'req-trace-6005');

-- 指派
INSERT INTO crm_presale_assignments
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   request_id, assignee_id, assignee_name_snapshot, assignee_department_snapshot, assignee_role,
   assigned_by, assigned_at, ended_at, is_current, batch_no, change_reason)
VALUES
  (6301, @tenant, @actor, @actor, @now, @now, NULL, 1, 6003, '01KYDVHC00000000000000000C', '张伟', '华东一区', 'TECHNICAL_SUPPORT', @actor, '2026-08-03 10:00:00.000', NULL, 1, 1, '测试指派'),
  (6302, @tenant, @actor, @actor, @now, @now, NULL, 1, 6003, '01KYDVHC00000000000000000F', '陈晨', '华东一区', 'TECHNICAL_SUPPORT', @actor, '2026-08-03 10:00:00.000', NULL, 1, 1, '测试指派'),
  (6303, @tenant, @actor, @actor, @now, @now, NULL, 1, 6004, '01KYDVHC00000000000000000G', '刘洋', '华南一区', 'TECHNICAL_SUPPORT', @actor, '2026-08-01 09:00:00.000', '2026-08-04 17:30:00.000', 0, 1, '已完成'),
  (6304, @tenant, @actor, @actor, @now, @now, NULL, 1, 6002, '01KYDVHC00000000000000000F', '陈晨', '华东一区', 'TECHNICAL_SUPPORT', @actor, '2026-08-04 10:30:00.000', NULL, 1, 2, '分派测试');

INSERT INTO crm_presale_assignment_events
  (id, event_id, tenant_id, request_id, assignment_id, event_type, recipient_person_id, person_name_snapshot,
   role_snapshot, change_reason, actor_id, request_id_trace, occurred_at)
VALUES
  (6401, 'evt-assign-6003-6301', @tenant, 6003, 6301, 'ADDED', '01KYDVHC00000000000000000C', '张伟', 'TECHNICAL_SUPPORT', '测试指派', @actor, 'trace-6003', '2026-08-03 10:00:00.000'),
  (6402, 'evt-assign-6003-6302', @tenant, 6003, 6302, 'ADDED', '01KYDVHC00000000000000000F', '陈晨', 'TECHNICAL_SUPPORT', '测试指派', @actor, 'trace-6003', '2026-08-03 10:00:00.000'),
  (6403, 'evt-assign-6004-6303', @tenant, 6004, 6303, 'ADDED', '01KYDVHC00000000000000000G', '刘洋', 'TECHNICAL_SUPPORT', '测试指派', @actor, 'trace-6004', '2026-08-01 09:00:00.000'),
  (6404, 'evt-assign-6004-6303-end', @tenant, 6004, 6303, 'REMOVED', '01KYDVHC00000000000000000G', '刘洋', 'TECHNICAL_SUPPORT', '已完成', @actor, 'trace-6004', '2026-08-04 17:30:00.000');

-- 进展记录
INSERT INTO crm_presale_progress_logs
  (id, tenant_id, request_id, author_id, content, link_url, progress_pct, idempotency_key, request_hash, created_at)
VALUES
  (6501, @tenant, 6003, '01KYDVHC00000000000000000C', '完成现场勘查，输出初步方案。', 'https://test.example/notes/1', 40, 'seed-progress-6003-1', @zero_hash, '2026-08-04 11:00:00.000'),
  (6502, @tenant, 6003, '01KYDVHC00000000000000000F', '完成设备清单核对。', '', 60, 'seed-progress-6003-2', @zero_hash, '2026-08-04 15:00:00.000'),
  (6503, @tenant, 6004, '01KYDVHC00000000000000000G', '交付数据安全咨询报告。', '', 100, 'seed-progress-6004-1', @zero_hash, '2026-08-04 17:00:00.000');

-- 状态日志
INSERT INTO crm_presale_status_logs
  (id, tenant_id, request_id, from_status, to_status, `trigger`, reason, operator_id, occurred_at, request_id_trace)
VALUES
  (6601, @tenant, 6001, 'APPROVAL_STARTING', 'PENDING_APPROVAL', 'APPROVAL_STARTED', '测试数据', @actor, '2026-08-04 10:00:00.000', 'trace-6001'),
  (6602, @tenant, 6002, 'PENDING_APPROVAL', 'APPROVED_PENDING_ASSIGNMENT', 'APPROVAL_CALLBACK', '审批通过', '01KYDVHC00000000000000000E', '2026-08-04 10:10:00.000', 'trace-6002'),
  (6603, @tenant, 6003, 'APPROVED_PENDING_ASSIGNMENT', 'EXECUTING', 'ASSIGNMENT_COMPLETED', '分派完成', @actor, '2026-08-03 10:00:00.000', 'trace-6003'),
  (6604, @tenant, 6004, 'EXECUTING', 'COMPLETED', 'COMPLETED', '交付完成', @actor, '2026-08-04 17:30:00.000', 'trace-6004'),
  (6605, @tenant, 6005, 'PENDING_APPROVAL', 'REJECTED', 'APPROVAL_CALLBACK', '预算不足', '01KYDVHC00000000000000000E', '2026-08-01 10:00:00.000', 'trace-6005');

-- 工时
INSERT INTO crm_presale_worklogs
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   worklog_no, request_id, person_id, department_snapshot, person_name_snapshot, work_start, work_end,
   raw_unit, raw_value, conversion_factor, work_hours, unit, work_site_address, work_content, remark,
   push_status, push_attempts, next_retry_at, last_error_summary, idempotency_key, request_hash,
   completed_task, voided_at)
VALUES
  (6701, @tenant, '01KYDVHC00000000000000000C', '01KYDVHC00000000000000000C', @now, @now, NULL, 1,
   'WL20260804TEST001', 6003, '01KYDVHC00000000000000000C', '华东一区', '张伟', '2026-08-04 09:00:00.000', '2026-08-04 12:00:00.000',
   'HOUR', 3.00, 1.00, 3.00, 'HOUR', '武汉市东湖高新区高新大道 1 号', '现场勘查', '',
   'SUCCESS', 1, NULL, '', 'seed-worklog-6701', @zero_hash, 0, NULL),
  (6702, @tenant, '01KYDVHC00000000000000000F', '01KYDVHC00000000000000000F', @now, @now, NULL, 1,
   'WL20260804TEST002', 6003, '01KYDVHC00000000000000000F', '华东一区', '陈晨', '2026-08-04 14:00:00.000', '2026-08-04 16:00:00.000',
   'HOUR', 2.00, 1.00, 2.00, 'HOUR', '武汉市东湖高新区高新大道 1 号', '方案设计', '',
   'RETRY_WAIT', 1, '2026-08-05 09:00:00.000', 'PMS 暂不可用', 'seed-worklog-6702', @zero_hash, 0, NULL),
  (6703, @tenant, '01KYDVHC00000000000000000G', '01KYDVHC00000000000000000G', @now, @now, NULL, 1,
   'WL20260804TEST003', 6004, '01KYDVHC00000000000000000G', '华南一区', '刘洋', '2026-08-04 09:00:00.000', '2026-08-04 12:00:00.000',
   'HOUR', 3.00, 1.00, 3.00, 'HOUR', '远程', '方案设计', '交付咨询报告',
   'DEAD_LETTER', 8, NULL, 'PMS 持续不可用', 'seed-worklog-6703', @zero_hash, 1, NULL);

-- 售前预警
INSERT INTO crm_presale_alerts
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   request_id, alert_type, rule_version, basis_at, due_at, status, recipient_kind, recipient_id, sent_at, read_at)
VALUES
  (6801, @tenant, @actor, @actor, @now, @now, NULL, 1, 6001, 'APPROVAL_NODE_1_OVERDUE', 1, '2026-08-04 10:00:00.000', '2026-08-05 10:00:00.000', 'UNREAD', 'USER', '01KYDVHC00000000000000000C', '2026-08-05 10:00:00.000', NULL),
  (6802, @tenant, @actor, @actor, @now, @now, NULL, 1, 6002, 'ASSIGNMENT_OVERDUE', 1, '2026-08-04 10:30:00.000', '2026-08-06 10:30:00.000', 'READ', 'PERSON', '01KYDVHC00000000000000000F', '2026-08-06 10:30:00.000', '2026-08-06 11:00:00.000'),
  (6803, @tenant, @actor, @actor, @now, @now, NULL, 1, 6003, 'EXECUTION_OVERDUE', 1, '2026-08-05 09:00:00.000', '2026-08-08 09:00:00.000', 'PENDING', 'PERSON', '01KYDVHC00000000000000000C', NULL, NULL);

-- 每日指标
INSERT INTO crm_presale_daily_metric_runs
  (id, tenant_id, window_start, window_end_exclusive, status, worker_id, row_count, source_max_updated_at, started_at, finished_at, error_summary)
VALUES
  (6901, @tenant, '2026-08-04', '2026-08-05', 'SUCCESS', 'presale-metrics-worker-test', 2, '2026-08-04 23:59:59.000', '2026-08-05 00:10:00.000', '2026-08-05 00:11:00.000', '');

INSERT INTO crm_presale_daily_metrics
  (id, tenant_id, metric_date, organization_id, person_id, person_name_snapshot, department_snapshot,
   opportunity_id, work_hours, request_count, worklog_count, pms_outbox_worklog_count, pms_success_count,
   source_max_updated_at, aggregated_at)
VALUES
  (7001, @tenant, '2026-08-04', '01KYDVHC000000000000000002', '01KYDVHC00000000000000000C', '张伟', '华东一区', 3003, 3.00, 1, 1, 1, 1, '2026-08-04 23:59:59.000', '2026-08-05 00:11:00.000'),
  (7002, @tenant, '2026-08-04', '01KYDVHC000000000000000002', '01KYDVHC00000000000000000F', '陈晨', '华东一区', 3003, 2.00, 1, 1, 1, 0, '2026-08-04 23:59:59.000', '2026-08-05 00:11:00.000');

-- 站内通知
INSERT INTO crm_notifications
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   source_event_id, type, opportunity_id, opportunity_version, opportunity_no, opportunity_name,
   request_id, request_no, assignment_id, progress_id, recipient_id, recipient_kind, title, body,
   target_path, status, read_at)
VALUES
  (7101, @tenant, @actor, @actor, @now, @now, NULL, 1, 'notify-owner-3001', 'OPPORTUNITY_OWNER_CHANGED', 3001, 1, 'SJ20260804TEST001', '工业互联网云平台等保测评', 0, '', 0, 0, '01KYDVHC00000000000000000D', 'PREVIOUS_OWNER', '商机负责人变更', '商机 3001 负责人已变更', '/customer-opportunity/opportunities?opportunity_id=3001', 'UNREAD', NULL),
  (7102, @tenant, @actor, @actor, @now, @now, NULL, 1, 'notify-assign-6003-6301', 'PRESALE_ASSIGNEE_ADDED', 3003, 1, 'SJ20260804TEST003', 'ERP与MES安全加固', 6003, 'TS20260804TEST003', 6301, 0, '01KYDVHC00000000000000000C', 'ASSIGNEE_ADDED', '售前指派通知', '您已被指派到售前申请 6003', '/customer-opportunity/presale?request_id=6003', 'READ', '2026-08-03 10:30:00.000'),
  (7103, @tenant, @actor, @actor, @now, @now, NULL, 1, 'notify-progress-6003-6501', 'PRESALE_PROGRESS_APPLICANT', 3003, 1, 'SJ20260804TEST003', 'ERP与MES安全加固', 6003, 'TS20260804TEST003', 0, 6501, '01KYDVHC00000000000000000F', 'PROGRESS_APPLICANT', '售前进展更新', '售前申请 6003 有新的进展', '/customer-opportunity/presale?request_id=6003', 'UNREAD', NULL);

SELECT CONCAT('CRM presale test data imported: requests=', (SELECT COUNT(*) FROM crm_presale_requests WHERE id BETWEEN 6001 AND 6005), ', engineers=', (SELECT COUNT(*) FROM crm_presale_engineers WHERE id BETWEEN 5001 AND 5005)) AS summary;
