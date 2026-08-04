-- 客户与商机管理 CRM 核心测试数据
-- 适用库：customer_opportunity（本地 docker：basic-platform-local-customer-mysql-1）
-- 幂等性：以固定 ID/编号插入，重复执行请先清空对应 ID 或改用 INSERT ... ON DUPLICATE KEY UPDATE。
-- 说明：联系方式使用当前本地密钥生成的 AES-256-GCM 密文；若更换 SENSITIVE_ENCRYPTION_KEY_BASE64 / SENSITIVE_HMAC_KEY_BASE64，
--       需要用 cmd/seed-demo-data 同款 codec 重新生成密文后再导入。

SET NAMES utf8mb4;
SET @tenant = '01J00000000000000000000000';
SET @zero_hash = '0000000000000000000000000000000000000000000000000000000000000000';
SET @actor = 'oidc-sub-demo-seed';
SET @actor_name = '测试数据导入';
SET @now = NOW(3);

-- 联系人密文常量（与现有种子同一密钥）
SET @phone_cipher_1 = X'B427F1E769B6939A9B654747FA483AF4A4C1967920D0F526E42985D5D7F6F54689BC33FDBAE2FB';
SET @phone_cipher_2 = X'EC856ED0D93051B8858CF950053B97EF3C5CC10691AC082CC7DAFDF34A0D84FDF4B95EA6FD05B7';
SET @phone_cipher_3 = X'ED04E1355978FE139080CB844B614B4885198FB6798A13AEA5A0B148E10E6AAFD64B8D86AEA4D9';
SET @phone_cipher_4 = X'613E281B6F006B06E5EFA4E86CDB2DB06CFA27906F013CE4C342E3685B3CE46F7C586485C86757';
SET @email_cipher_1 = X'7D0B65E6A179B81B80D2C21BA1FF07FCBB6181A615B3BC6603441815CD15BDAED51983FDC024698BB83457DF7CA617E28BC99E703FF6694814DB1C';
SET @email_cipher_2 = X'2DE3EFA8EBBB968998279F87BAF65A34BFE31A4EBAF3A451BA9A34E463BFE9AB9CEF9695F943C997BFE66BC6A6CEE65460AB6832E3CDA710';

-- ============ 客户主数据 ============
INSERT INTO crm_customers
  (id, tenant_id, customer_no, name, normalized_name, unified_credit_code_cipher, unified_credit_code_hmac,
   customer_type, industry, region, owner_user_id, owner_org_id, status, end_date, merged_into_id,
   created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (2001, @tenant, 'KH20260804TEST001', '云启科技（广州）有限公司', '云启科技广州有限公司', NULL, NULL,
   '业主', '软件', '华南', 'oidc-sub-chen-haoran', 'org-demo-east1', 'ACTIVE', NULL, NULL,
   @actor, @actor, @now, @now, NULL, 1),
  (2002, @tenant, 'KH20260804TEST002', '明德医疗集团股份有限公司', '明德医疗集团股份有限公司', NULL, NULL,
   '三方', '医疗', '华北', 'oidc-sub-liu-ming', 'org-demo-east1', 'ACTIVE', NULL, NULL,
   @actor, @actor, @now, @now, NULL, 1),
  (2003, @tenant, 'KH20260804TEST003', '澄海物流有限公司', '澄海物流有限公司', NULL, NULL,
   '三方', '物流', '华东', 'oidc-sub-wang-jianguo', 'org-demo-east1', 'ACTIVE', NULL, NULL,
   @actor, @actor, @now, @now, NULL, 1),
  (2004, @tenant, 'KH20260804TEST004', '华岳智能制造有限公司', '华岳智能制造有限公司', NULL, NULL,
   '业主', '制造', '华中', 'oidc-sub-zhao-xiaoyan', 'org-demo-south1', 'ACTIVE', NULL, NULL,
   @actor, @actor, @now, @now, NULL, 1);

-- ============ 联系人 ============
INSERT INTO crm_customer_contacts
  (id, tenant_id, customer_id, name, phone_cipher, phone_masked, email_cipher, email_masked,
   is_registration, sort_order, created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (2101, @tenant, 2001, '周正', @phone_cipher_1, '138****6789', @email_cipher_1, 'z***@yunqi.example.com', 1, 1, @actor, @actor, @now, @now, NULL, 1),
  (2102, @tenant, 2001, '郑敏', @phone_cipher_2, '139****6990', @email_cipher_2, 'z***@yunqi.example.com', 0, 2, @actor, @actor, @now, @now, NULL, 1),
  (2103, @tenant, 2002, '孙立', @phone_cipher_3, '138****6890', NULL, '', 1, 1, @actor, @actor, @now, @now, NULL, 1),
  (2104, @tenant, 2002, '吴倩', @phone_cipher_4, '137****6788', NULL, '', 0, 2, @actor, @actor, @now, @now, NULL, 1),
  (2105, @tenant, 2003, '冯远', @phone_cipher_3, '138****6890', NULL, '', 1, 1, @actor, @actor, @now, @now, NULL, 1),
  (2106, @tenant, 2004, '何平', @phone_cipher_1, '138****6789', NULL, '', 1, 1, @actor, @actor, @now, @now, NULL, 1);

-- ============ 关键干系人 ============
INSERT INTO crm_customer_stakeholders
  (id, tenant_id, customer_id, name, role_title, influence, relationship_summary,
   phone_cipher, phone_masked, email_cipher, email_masked, sort_order,
   created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (2201, @tenant, 2001, '周正', '信息科技部总经理', 'HIGH', '云平台与安全建设决策人', @phone_cipher_1, '138****6789', @email_cipher_1, 'z***@yunqi.example.com', 1, @actor, @actor, @now, @now, NULL, 1),
  (2202, @tenant, 2001, '郑敏', '采购部经理', 'MEDIUM', '采购与商务流程执行人', @phone_cipher_2, '139****6990', @email_cipher_2, 'z***@yunqi.example.com', 2, @actor, @actor, @now, @now, NULL, 1),
  (2203, @tenant, 2002, '孙立', '信息中心主任', 'HIGH', '医疗信息化项目负责人', @phone_cipher_3, '138****6890', NULL, '', 1, @actor, @actor, @now, @now, NULL, 1),
  (2204, @tenant, 2004, '何平', '智能制造数字化总监', 'HIGH', '工业安全项目负责人', @phone_cipher_1, '138****6789', NULL, '', 1, @actor, @actor, @now, @now, NULL, 1);

-- ============ 系统与备案 ============
INSERT INTO crm_customer_systems
  (id, tenant_id, customer_id, name, protection_level, application_scenario, filing_no, grading_date,
   filing_status, sort_order, created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (2301, @tenant, 2001, '工业互联网云平台', 'LEVEL_3', '工业云服务', '440100-90001', '2026-01-15', 'FILED', 1, @actor, @actor, @now, @now, NULL, 1),
  (2302, @tenant, 2001, '数据中台', 'LEVEL_3', '数据治理', '440100-90002', '2026-02-10', 'FILED', 2, @actor, @actor, @now, @now, NULL, 1),
  (2303, @tenant, 2002, '医院信息系统', 'LEVEL_3', '医疗信息化', '110100-70001', '2025-12-20', 'FILED', 1, @actor, @actor, @now, @now, NULL, 1),
  (2304, @tenant, 2004, 'ERP系统', 'LEVEL_3', '企业资源计划', '420100-30001', '2026-03-08', 'FILING', 1, @actor, @actor, @now, @now, NULL, 1),
  (2305, @tenant, 2004, '生产制造执行系统', 'LEVEL_3', '生产执行', '420100-30002', '2026-03-20', 'FILED', 2, @actor, @actor, @now, @now, NULL, 1);

-- ============ 客户跟进 ============
INSERT INTO crm_customer_followups
  (id, tenant_id, customer_id, type, content, followed_at, followed_by, next_follow_at,
   created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (2401, @tenant, 2001, 'PHONE', '初次拜访，沟通工业云平台等保测评需求。', '2026-08-01 10:00:00.000', 'oidc-sub-chen-haoran', '2026-08-10 09:00:00.000', @actor, @actor, @now, @now, NULL, 1),
  (2402, @tenant, 2001, 'VISIT', '现场调研数据中心与云平台边界。', '2026-08-02 14:00:00.000', 'oidc-sub-chen-haoran', NULL, @actor, @actor, @now, @now, NULL, 1),
  (2403, @tenant, 2002, 'PHONE', '确认医院信息系统测评范围与时间窗口。', '2026-07-30 15:00:00.000', 'oidc-sub-liu-ming', '2026-08-08 11:00:00.000', @actor, @actor, @now, @now, NULL, 1),
  (2404, @tenant, 2004, 'VISIT', '华岳制造现场踏勘，评估生产网安全现状。', '2026-07-28 09:30:00.000', 'oidc-sub-zhao-xiaoyan', '2026-08-12 10:00:00.000', @actor, @actor, @now, @now, NULL, 1);

-- ============ 商机 ============
INSERT INTO crm_opportunities
  (id, tenant_id, opportunity_no, name, customer_id, type, source, expected_amount, expected_sign_date,
   requirement_summary, system_count, pain_points, competitor_info, owner_user_id, owner_org_id,
   current_stage, opp_status, contract_ref, lost_reason, terminal_pending_type, stage_changed_at,
   external_status_changed_at, end_date, status_before_void, created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (3001, @tenant, 'SJ20260804TEST001', '工业互联网云平台等保测评', 2001, '新购', '自主开发', 68.00, '2026-09-30',
   '云平台三级等保测评、整改协助与安全运营。', 2, '生产系统不能中断，测评窗口有限。', '安恒信息报价偏高。', 'oidc-sub-chen-haoran', 'org-demo-east1',
   '初步接触', 'FOLLOWING', NULL, NULL, 'NONE', '2026-08-01 10:00:00.000', NULL, NULL, NULL, @actor, @actor, @now, @now, NULL, 1),
  (3002, @tenant, 'SJ20260804TEST002', '医院信息系统等保测评', 2002, '新购', '老客户转介绍', 92.00, '2026-10-15',
   '医院核心信息系统三级测评与整改。', 1, '临床业务 7x24 小时在线。', '两家本地机构进入比价。', 'oidc-sub-liu-ming', 'org-demo-east1',
   '需求沟通', 'FOLLOWING', NULL, NULL, 'NONE', '2026-07-30 15:00:00.000', NULL, NULL, NULL, @actor, @actor, @now, @now, NULL, 1),
  (3003, @tenant, 'SJ20260804TEST003', 'ERP与MES安全加固', 2004, '服务', '渠道合作', 45.50, '2026-11-20',
   'ERP、MES 安全加固与上线前渗透测试。', 2, '车间网络与办公网互联风险高。', '无强竞品。', 'oidc-sub-wang-jianguo', 'org-demo-east1',
   '方案制定', 'FOLLOWING', NULL, NULL, 'NONE', '2026-07-28 09:30:00.000', NULL, NULL, NULL, @actor, @actor, @now, @now, NULL, 1),
  (3004, @tenant, 'SJ20260804TEST004', '数据中台安全咨询', 2001, '服务', '自主开发', 28.00, '2026-09-10',
   '数据分级分类与安全合规差距咨询。', 1, '数据量大，分类规则复杂。', '绿盟进入二轮比价。', 'oidc-sub-zhao-xiaoyan', 'org-demo-south1',
   '报价', 'FOLLOWING', NULL, NULL, 'NONE', '2026-08-02 16:00:00.000', NULL, NULL, NULL, @actor, @actor, @now, @now, NULL, 1),
  (3005, @tenant, 'SJ20260804TEST005', '互联网医院渗透测试', 2002, '服务', '公开招标', 33.00, '2026-12-31',
   '互联网医院系统渗透测试与代码审计。', 1, '上线时间紧，测试需按版本锁定。', '两家本地测评机构参与投标。', 'oidc-sub-li-tingfang', 'org-demo-south1',
   '投标', 'FOLLOWING', NULL, NULL, 'NONE', '2026-07-26 11:00:00.000', NULL, NULL, NULL, @actor, @actor, @now, @now, NULL, 1),
  (3006, @tenant, 'SJ20260804TEST006', '华岳生产网等保测评', 2004, '新购', '老客户转介绍', 120.00, '2026-08-18',
   '生产制造执行系统等保三级测评。', 2, '停机窗口短。', '无。', 'oidc-sub-chen-haoran', 'org-demo-east1',
   '已签约', 'CLOSED', 'HT-2026-TEST1001', NULL, 'NONE', '2026-07-20 10:00:00.000', NULL, '2026-08-18', NULL, @actor, @actor, @now, @now, NULL, 1),
  (3007, @tenant, 'SJ20260804TEST007', '澄海TMS系统安全评估', 2003, '服务', '线索', 16.00, '2026-08-05',
   '运输管理系统安全评估与加固建议。', 1, '预算有限。', '本地集成商低价竞争。', 'oidc-sub-liu-ming', 'org-demo-east1',
   '失败', 'CLOSED', NULL, '预算不足', 'NONE', '2026-07-25 14:00:00.000', NULL, '2026-07-25', NULL, @actor, @actor, @now, @now, NULL, 1),
  (3008, @tenant, 'SJ20260804TEST008', '云启供应链安全专项', 2001, '新购', '自主开发', 55.00, '2026-10-31',
   '供应链安全专项评估。', 1, '供应商众多，边界难定义。', '无。', 'oidc-sub-zhao-xiaoyan', 'org-demo-south1',
   '初步接触', 'FOLLOWING', NULL, NULL, 'NONE', '2026-08-03 09:00:00.000', NULL, NULL, NULL, @actor, @actor, @now, @now, NULL, 1);

-- ============ 商机阶段日志 ============
INSERT INTO crm_opportunity_stage_logs
  (id, tenant_id, opportunity_id, from_stage, to_stage, source, source_id, reason, contract_ref, lost_reason,
   pending_type, operator_id, changed_at, request_id)
VALUES
  (3101, @tenant, 3001, '初步接触', '初步接触', 'MANUAL', 'seed-test-stage-3001', '测试数据初始化', NULL, NULL, 'NONE', @actor, '2026-08-01 10:00:00.000', 'req-test-3001'),
  (3102, @tenant, 3002, '初步接触', '需求沟通', 'MANUAL', 'seed-test-stage-3002', '需求调研完成', NULL, NULL, 'NONE', @actor, '2026-07-30 15:00:00.000', 'req-test-3002'),
  (3103, @tenant, 3003, '需求沟通', '方案制定', 'MANUAL', 'seed-test-stage-3003', '完成初步方案', NULL, NULL, 'NONE', @actor, '2026-07-28 09:30:00.000', 'req-test-3003'),
  (3104, @tenant, 3004, '需求沟通', '报价', 'MANUAL', 'seed-test-stage-3004', '提交正式报价', NULL, NULL, 'NONE', @actor, '2026-08-02 16:00:00.000', 'req-test-3004'),
  (3105, @tenant, 3005, '报价', '投标', 'MANUAL', 'seed-test-stage-3005', '参与公开招标', NULL, NULL, 'NONE', @actor, '2026-07-26 11:00:00.000', 'req-test-3005'),
  (3106, @tenant, 3006, '报价', '已签约', 'MANUAL', 'seed-test-stage-3006', '签约完成', 'HT-2026-TEST1001', NULL, 'NONE', @actor, '2026-07-20 10:00:00.000', 'req-test-3006'),
  (3107, @tenant, 3007, '报价', '失败', 'MANUAL', 'seed-test-stage-3007', '客户预算不足', NULL, '预算不足', 'NONE', @actor, '2026-07-25 14:00:00.000', 'req-test-3007'),
  (3108, @tenant, 3008, '初步接触', '初步接触', 'MANUAL', 'seed-test-stage-3008', '测试数据初始化', NULL, NULL, 'NONE', @actor, '2026-08-03 09:00:00.000', 'req-test-3008');

-- ============ 商机团队 ============
INSERT INTO crm_opportunity_members
  (id, tenant_id, created_by, updated_by, created_at, updated_at, deleted_at, version,
   opportunity_id, user_id, role, is_active, ended_at)
VALUES
  (3201, @tenant, @actor, @actor, @now, @now, NULL, 1, 3001, 'oidc-sub-wang-jianguo', 'TECHNICAL_SUPPORT', 1, NULL),
  (3202, @tenant, @actor, @actor, @now, @now, NULL, 1, 3002, 'oidc-sub-chen-haoran', 'SALES_SUPPORT', 1, NULL),
  (3203, @tenant, @actor, @actor, @now, @now, NULL, 1, 3003, 'oidc-sub-zhao-xiaoyan', 'TECHNICAL_SUPPORT', 1, NULL),
  (3204, @tenant, @actor, @actor, @now, @now, NULL, 1, 3004, 'oidc-sub-liu-ming', 'BUSINESS_SUPPORT', 1, NULL),
  (3205, @tenant, @actor, @actor, @now, @now, NULL, 1, 3005, 'oidc-sub-wang-jianguo', 'TECHNICAL_SUPPORT', 1, NULL),
  (3206, @tenant, @actor, @actor, @now, @now, NULL, 1, 3006, 'oidc-sub-li-tingfang', 'SALES_SUPPORT', 1, NULL);

-- ============ 团队成员任期账本 ============
INSERT INTO crm_opportunity_member_terms
  (id, tenant_id, opportunity_id, member_id, user_id, role, started_at, snapshot_at, active_at_snapshot,
   ended_at, started_by, ended_by, source_kind)
VALUES
  (3301, @tenant, 3001, 3201, 'oidc-sub-wang-jianguo', 'TECHNICAL_SUPPORT', '2026-08-01 10:00:00.000', NULL, NULL, NULL, @actor, NULL, 'RECORDED'),
  (3302, @tenant, 3002, 3202, 'oidc-sub-chen-haoran', 'SALES_SUPPORT', '2026-07-30 15:00:00.000', NULL, NULL, NULL, @actor, NULL, 'RECORDED'),
  (3303, @tenant, 3003, 3203, 'oidc-sub-zhao-xiaoyan', 'TECHNICAL_SUPPORT', '2026-07-28 09:30:00.000', NULL, NULL, NULL, @actor, NULL, 'RECORDED'),
  (3304, @tenant, 3004, 3204, 'oidc-sub-liu-ming', 'BUSINESS_SUPPORT', '2026-08-02 16:00:00.000', NULL, NULL, NULL, @actor, NULL, 'RECORDED'),
  (3305, @tenant, 3005, 3205, 'oidc-sub-wang-jianguo', 'TECHNICAL_SUPPORT', '2026-07-26 11:00:00.000', NULL, NULL, NULL, @actor, NULL, 'RECORDED'),
  (3306, @tenant, 3006, 3206, 'oidc-sub-li-tingfang', 'SALES_SUPPORT', '2026-07-20 10:00:00.000', NULL, NULL, NULL, @actor, NULL, 'RECORDED');

-- ============ 商机跟进 ============
INSERT INTO crm_opportunity_followups
  (id, tenant_id, opportunity_id, type, content, followed_at, followed_by, next_follow_at,
   created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (3401, @tenant, 3001, 'PHONE', '确认测评范围与报价预期。', '2026-08-02 10:30:00.000', 'oidc-sub-chen-haoran', '2026-08-12 09:00:00.000', @actor, @actor, @now, @now, NULL, 1),
  (3402, @tenant, 3002, 'VISIT', '医院现场需求确认。', '2026-07-31 14:00:00.000', 'oidc-sub-liu-ming', '2026-08-09 11:00:00.000', @actor, @actor, @now, @now, NULL, 1),
  (3403, @tenant, 3004, 'EMAIL', '发送正式报价单。', '2026-08-03 09:00:00.000', 'oidc-sub-zhao-xiaoyan', '2026-08-10 09:00:00.000', @actor, @actor, @now, @now, NULL, 1),
  (3404, @tenant, 3005, 'PHONE', '投标答疑完成。', '2026-07-27 16:00:00.000', 'oidc-sub-li-tingfang', '2026-08-11 10:00:00.000', @actor, @actor, @now, @now, NULL, 1);

-- ============ 商机附件（仅元数据，对象存储引用） ============
INSERT INTO crm_opportunity_attachments
  (id, tenant_id, public_id, opportunity_id, object_key, object_version, file_name, size_bytes, mime_type,
   sha256, scan_status, scan_reference, upload_expires_at, upload_lease_until, finalize_lease_until,
   uploaded_at, scanned_at, create_actor_id, create_idempotency_key, create_request_hash,
   created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (4001, @tenant, 'att-test-0000000000000001', 3001, 'test-object/opportunity-3001/方案.pdf', 'v1', '工业互联网云平台等保测评方案.pdf', 1024000, 'application/pdf',
   'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'CLEAN', 'scan-ref-3001-1', NULL, NULL, NULL,
   '2026-08-02 12:00:00.000', '2026-08-02 12:05:00.000', @actor, 'seed-att-3001', @zero_hash, @actor, @actor, @now, @now, NULL, 1),
  (4002, @tenant, 'att-test-0000000000000002', 3004, 'test-object/opportunity-3004/报价单.pdf', 'v1', '数据中台安全咨询报价单.pdf', 512000, 'application/pdf',
   'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'CLEAN', 'scan-ref-3004-1', NULL, NULL, NULL,
   '2026-08-03 10:00:00.000', '2026-08-03 10:05:00.000', @actor, 'seed-att-3004', @zero_hash, @actor, @actor, @now, @now, NULL, 1);

-- ============ 商机外部报价/投标状态 ============
INSERT INTO crm_opportunity_external_links
  (id, tenant_id, opportunity_id, type, source_id, status, amount, changed_at, snapshot_json, created_at)
VALUES
  (4101, @tenant, 3004, '报价', 'qb-test-quotation-3004', 'QUOTATION_SUBMITTED', 28.00, '2026-08-02 17:00:00.000', JSON_OBJECT('source','test','status','QUOTATION_SUBMITTED'), @now),
  (4102, @tenant, 3005, '投标', 'qb-test-bid-3005', 'BID_SUBMITTED', 33.00, '2026-07-26 12:00:00.000', JSON_OBJECT('source','test','status','BID_SUBMITTED'), @now);

-- ============ 合同转交投递尝试 ============
INSERT INTO crm_contract_transfer_attempts
  (id, tenant_id, source_event_id, attempt_no, result, contract_intake_id, response_code, error_summary, attempted_at)
VALUES
  (4201, @tenant, 'seed-transfer-3006-1', 1, 'SENT', 'intake-3006-1', '202', '', '2026-07-20 11:00:00.000'),
  (4202, @tenant, 'seed-transfer-3006-2', 1, 'RETRY_WAIT', '', '503', 'contract service unavailable', '2026-07-20 11:05:00.000');

-- ============ 审计事件 ============
INSERT INTO crm_audit_events
  (id, event_id, tenant_id, application_code, module, operation, resource_type, resource_id, actor_id,
   actor_name_snapshot, before_json, after_json, reason, result, request_id, occurred_at)
VALUES
  (3501, 'audit-test-2001-create', @tenant, 'customer_and_opportunity', 'customer', 'CREATE', 'customer', '2001', @actor, @actor_name, NULL, JSON_OBJECT('customer_id',2001), '测试数据导入', 'SUCCESS', 'req-audit-2001', @now),
  (3502, 'audit-test-2002-create', @tenant, 'customer_and_opportunity', 'customer', 'CREATE', 'customer', '2002', @actor, @actor_name, NULL, JSON_OBJECT('customer_id',2002), '测试数据导入', 'SUCCESS', 'req-audit-2002', @now),
  (3503, 'audit-test-3001-create', @tenant, 'customer_and_opportunity', 'opportunity', 'CREATE', 'opportunity', '3001', @actor, @actor_name, NULL, JSON_OBJECT('opportunity_id',3001), '测试数据导入', 'SUCCESS', 'req-audit-3001', @now),
  (3504, 'audit-test-3006-stage', @tenant, 'customer_and_opportunity', 'opportunity', 'STAGE_CHANGE', 'opportunity', '3006', @actor, @actor_name, NULL, JSON_OBJECT('stage','已签约'), '测试数据导入', 'SUCCESS', 'req-audit-3006', @now);

-- ============ 客户变更日志 ============
INSERT INTO crm_customer_change_logs
  (id, tenant_id, customer_id, field_name, before_json, after_json, reason, operator_id, request_id, occurred_at)
VALUES
  (3601, @tenant, 2001, 'region', '"华南"', '"华南"', '测试数据导入', @actor, 'req-change-2001', @now),
  (3602, @tenant, 2004, 'industry', '"制造"', '"制造"', '测试数据导入', @actor, 'req-change-2004', @now);

-- ============ 客户合并日志 ============
INSERT INTO crm_customer_merge_logs
  (id, tenant_id, source_customer_id, target_customer_id, source_version, target_version, migrated_counts_json,
   reason, operator_id, request_id, occurred_at)
VALUES
  (3701, @tenant, 2003, 2001, 1, 1, JSON_OBJECT('contacts',1,'opportunities',0,'followups',0), '测试数据合并示例', @actor, 'req-merge-test-1', @now);

-- ============ 客户导入任务与行 ============
INSERT INTO crm_customer_import_jobs
  (id, tenant_id, job_no, actor_id, status, reason, total_rows, importable_rows, warning_rows, error_rows,
   succeeded_rows, failed_rows, expires_at, completed_at, commit_request_version, commit_idempotency_key,
   locked_by, locked_until, created_at, updated_at, version)
VALUES
  (3801, @tenant, 'IMP-TEST-20260804-001', @actor, 'COMPLETED', '测试导入完成', 3, 3, 0, 0, 2, 1,
   '2026-09-01 00:00:00.000', '2026-08-04 11:00:00.000', 1, 'seed-commit-imp-1', '', NULL, @now, @now, 1);

INSERT INTO crm_customer_import_rows
  (id, tenant_id, job_id, row_no, status, command_cipher, error_column, error_code, error_message,
   customer_id, customer_no, created_at, updated_at)
VALUES
  (3901, @tenant, 3801, 1, 'IMPORTED', NULL, '', '', '', 2001, 'KH20260804TEST001', @now, @now),
  (3902, @tenant, 3801, 2, 'IMPORTED', NULL, '', '', '', 2002, 'KH20260804TEST002', @now, @now),
  (3903, @tenant, 3801, 3, 'FAILED', NULL, 'phone', 'INVALID_PHONE', '手机号格式错误', NULL, '', @now, @now);

-- ============ 完成提示 ============
SELECT CONCAT('CRM core test data imported: customers=', (SELECT COUNT(*) FROM crm_customers WHERE id BETWEEN 2001 AND 2004), ', opportunities=', (SELECT COUNT(*) FROM crm_opportunities WHERE id BETWEEN 3001 AND 3008)) AS summary;
