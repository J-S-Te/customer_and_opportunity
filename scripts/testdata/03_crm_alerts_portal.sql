-- CRM 阶段预警、站内通知、Outbox、Portal 邀请/身份映射测试数据
-- 适用库：customer_opportunity（本地 docker：basic-platform-local-customer-mysql-1）
SET NAMES utf8mb4;
SET @tenant = '01J00000000000000000000000';
SET @zero_hash = '0000000000000000000000000000000000000000000000000000000000000000';
SET @actor = '01KYDVHC00000000000000000C';
SET @actor_name = '张伟';
SET @now = NOW(3);

-- 商机阶段预警规则（5 个非终态阶段）
INSERT INTO crm_opportunity_stage_alert_rules
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version, stage, threshold_hours, enabled, config_version)
VALUES
  (8001, @tenant, @actor, @actor, @now, @now, NULL, 1, '初步接触', 72, 1, 1),
  (8002, @tenant, @actor, @actor, @now, @now, NULL, 1, '需求沟通', 72, 1, 1),
  (8003, @tenant, @actor, @actor, @now, @now, NULL, 1, '方案制定', 96, 1, 1),
  (8004, @tenant, @actor, @actor, @now, @now, NULL, 1, '报价', 48, 1, 1),
  (8005, @tenant, @actor, @actor, @now, @now, NULL, 1, '投标', 48, 1, 1);

INSERT INTO crm_opportunity_stage_alert_rule_versions
  (id, tenant_id, rule_id, stage, threshold_hours, enabled, config_version, changed_by, request_id, changed_at)
VALUES
  (8101, @tenant, 8001, '初步接触', 72, 1, 1, @actor, 'req-rule-8001', @now),
  (8102, @tenant, 8002, '需求沟通', 72, 1, 1, @actor, 'req-rule-8002', @now),
  (8103, @tenant, 8003, '方案制定', 96, 1, 1, @actor, 'req-rule-8003', @now),
  (8104, @tenant, 8004, '报价', 48, 1, 1, @actor, 'req-rule-8004', @now),
  (8105, @tenant, 8005, '投标', 48, 1, 1, @actor, 'req-rule-8005', @now);

-- 商机阶段预警实例
INSERT INTO crm_opportunity_stage_alerts
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   opportunity_id, stage, threshold_version, basis_at, due_at, status, recipient_id, sent_at, read_at)
VALUES
  (8201, @tenant, @actor, @actor, @now, @now, NULL, 1, 3001, '初步接触', 1, '2026-08-01 10:00:00.000', '2026-08-04 10:00:00.000', 'UNREAD', '01KYDVHC00000000000000000C', '2026-08-04 10:00:00.000', NULL),
  (8202, @tenant, @actor, @actor, @now, @now, NULL, 1, 3002, '需求沟通', 1, '2026-07-30 15:00:00.000', '2026-08-02 15:00:00.000', 'READ', '01KYDVHC00000000000000000D', '2026-08-02 15:00:00.000', '2026-08-02 16:00:00.000'),
  (8203, @tenant, @actor, @actor, @now, @now, NULL, 1, 3004, '报价', 1, '2026-08-02 16:00:00.000', '2026-08-04 16:00:00.000', 'PENDING', '01KYDVHC00000000000000000G', NULL, NULL),
  (8204, @tenant, @actor, @actor, @now, @now, NULL, 1, 3005, '投标', 1, '2026-07-26 11:00:00.000', '2026-07-28 11:00:00.000', 'CANCELLED', '01KYDVHC00000000000000000E', NULL, NULL);

-- 站内通知（负责人变更 + 售前指派/进展，补充更多收件人）
INSERT INTO crm_notifications
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   source_event_id, type, opportunity_id, opportunity_version, opportunity_no, opportunity_name,
   request_id, request_no, assignment_id, progress_id, recipient_id, recipient_kind, title, body,
   target_path, status, read_at)
VALUES
  (8301, @tenant, @actor, @actor, @now, @now, NULL, 1, 'notify-owner-3006', 'OPPORTUNITY_OWNER_CHANGED', 3006, 1, 'SJ20260804TEST006', '华岳生产网等保测评', 0, '', 0, 0, '01KYDVHC00000000000000000E', 'NEW_OWNER', '商机负责人变更', '您已成为商机 3006 的负责人', '/customer-opportunity/opportunities?opportunity_id=3006', 'UNREAD', NULL),
  (8302, @tenant, @actor, @actor, @now, @now, NULL, 1, 'notify-assign-6002-6304', 'PRESALE_ASSIGNEE_ADDED', 3002, 1, 'SJ20260804TEST002', '医院信息系统等保测评', 6002, 'TS20260804TEST002', 6304, 0, '01KYDVHC00000000000000000F', 'ASSIGNEE_ADDED', '售前指派通知', '您已被指派到售前申请 6002', '/customer-opportunity/presale?request_id=6002', 'READ', '2026-08-04 11:00:00.000'),
  (8303, @tenant, @actor, @actor, @now, @now, NULL, 1, 'notify-progress-6004-6503', 'PRESALE_PROGRESS_APPLICANT', 3004, 1, 'SJ20260804TEST004', '数据中台安全咨询', 6004, 'TS20260804TEST004', 0, 6503, '01KYDVHC00000000000000000G', 'PROGRESS_APPLICANT', '售前进展更新', '售前申请 6004 已完成', '/customer-opportunity/presale?request_id=6004', 'UNREAD', NULL);

-- Outbox 事件（合同签单、售前审批启动）
INSERT INTO crm_outbox_events
  (id, event_id, tenant_id, event_type, aggregate_type, aggregate_id, payload, status, retry_count,
   next_retry_at, locked_by, locked_until, last_error_summary, created_at, sent_at)
VALUES
  (8401, 'outbox-test-signed-3006', @tenant, 'OPPORTUNITY_SIGNED', 'opportunity', '3006', JSON_OBJECT('opportunity_id',3006,'contract_ref','HT-2026-TEST1001'), 'PENDING', 0, NULL, '', NULL, '', @now, NULL),
  (8402, 'outbox-test-presale-6001', @tenant, 'PRESALE_APPROVAL_STARTED', 'presale_request', '6001', JSON_OBJECT('request_id',6001,'request_no','TS20260804TEST001'), 'PENDING', 0, NULL, '', NULL, '', @now, NULL);

-- Portal 邀请与身份映射（仅业务数据，不创建登录账号/角色）
INSERT INTO crm_portal_identity_links
  (id, tenant_id, customer_id, contact_id, platform_user_id, portal_account_id, status, provisioned_at,
   last_verified_at, created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (3, @tenant, 2001, 2101, 'test-platform-user-2001', 'PA-TEST-2001', 'ACTIVE', '2026-08-04 10:00:00.000', '2026-08-04 10:00:00.000', @actor, @actor, @now, @now, NULL, 1);

INSERT INTO crm_portal_invites
  (id, tenant_id, invite_no, customer_id, contact_id, platform_user_id, account_no, portal_account_id,
   token_hash, status, expires_at, used_at, revoked_at, revoked_reason, created_by, updated_by,
   created_at, updated_at, deleted_at, version)
VALUES
  (2, @tenant, 'PI-TEST-2001-PENDING', 2001, 2101, 'test-platform-user-2001', '', 'PA-TEST-2001',
   '1111111111111111111111111111111111111111111111111111111111111111', 'PENDING', '2026-08-18 10:00:00.000', NULL, NULL, '', @actor, @actor, @now, @now, NULL, 1),
  (3, @tenant, 'PI-TEST-2001-REVOKED', 2001, 2101, 'test-platform-user-2001', '', 'PA-TEST-2001',
   '2222222222222222222222222222222222222222222222222222222222222222', 'REVOKED', '2026-08-10 10:00:00.000', NULL, '2026-08-03 10:00:00.000', '重复邀请', @actor, @actor, @now, @now, NULL, 1);

SELECT CONCAT('CRM alerts/portal test data imported: alert_rules=', (SELECT COUNT(*) FROM crm_opportunity_stage_alert_rules WHERE id BETWEEN 8001 AND 8005), ', notifications=', (SELECT COUNT(*) FROM crm_notifications WHERE id BETWEEN 7101 AND 8303)) AS summary;
