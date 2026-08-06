-- 客户自助门户（Portal）测试数据
-- 适用库：customer_portal（本地 docker：basic-platform-local-portal-mysql-1）
-- 不创建登录账号、会话或角色；仅复用已存在的 EXT-01KZ1KD4XSR8ZFBP5T41PPDZTQ 身份映射。
SET NAMES utf8mb4;
SET @tenant = '01J00000000000000000000000';
SET @zero_hash = '0000000000000000000000000000000000000000000000000000000000000000';
SET @actor = '01KYDVHC00000000000000000C';
SET @account = 'EXT-01KZ1KD4XSR8ZFBP5T41PPDZTQ';
SET @customer = 5;
SET @project = 'PRJ-TEST-2026-001';
SET @now = NOW(3);
SET @object_cipher = X'B427F1E769B6939A9B654747FA483AF4A4C1967920D0F526E42985D5D7F6F54689BC33FDBAE2FB';

-- ============ 项目 ============
INSERT INTO portal_project_snapshots
  (id, tenant_id, project_id, customer_id, project_name, contract_no, status, progress_pct, current_stage,
   expected_end_date, `delayed`, manager_name_snapshot, manager_contact_masked, manager_portal_account_id,
   source_updated_at, synced_at, raw_version, created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (9001, @tenant, @project, @customer, '华兴证券安全运营服务项目', 'HT-2026-0088', 'IN_PROGRESS', 60, '安全运营', '2026-12-31', 0,
   '项目经理-测试', '138****0000', 'PADMIN1', '2026-08-04 09:00:00.000', @now, 'v1', @actor, @actor, @now, @now, NULL, 1);

INSERT INTO portal_project_milestones
  (id, tenant_id, customer_id, project_id, stage_code, stage_name, status, planned_at, completed_at, sort_no)
VALUES
  (9101, @tenant, @customer, @project, 'KICKOFF', '项目启动', 'COMPLETED', '2026-08-01 09:00:00.000', '2026-08-01 12:00:00.000', 1),
  (9102, @tenant, @customer, @project, 'ASSESSMENT', '安全评估', 'IN_PROGRESS', '2026-08-05 09:00:00.000', NULL, 2),
  (9103, @tenant, @customer, @project, 'REPORT', '报告交付', 'PENDING', '2026-12-20 09:00:00.000', NULL, 3);

INSERT INTO portal_project_activities
  (id, tenant_id, customer_id, project_id, source_activity_id, type, content, occurred_at)
VALUES
  (9201, @tenant, @customer, @project, 'act-test-1', 'MILESTONE', '项目启动会完成', '2026-08-01 12:00:00.000'),
  (9202, @tenant, @customer, @project, 'act-test-2', 'REPORT', '已完成边界梳理，准备测评', '2026-08-04 10:00:00.000');

INSERT INTO portal_project_team
  (id, tenant_id, customer_id, project_id, person_ref, name, role, contact_masked)
VALUES
  (9301, @tenant, @customer, @project, '01KYDVHC00000000000000000C', '张伟', '项目经理', '138****6789'),
  (9302, @tenant, @customer, @project, '01KYDVHC00000000000000000F', '陈晨', '安全工程师', '138****6890');

-- ============ 项目沟通 ============
INSERT INTO portal_project_conversations
  (id, public_id, tenant_id, customer_id, project_id, customer_account_id, manager_account_id_snapshot,
   manager_name_snapshot, create_idempotency_key, create_request_hash, last_message_at, created_at, updated_at, version)
VALUES
  (9401, 'conv-test-0000000001', @tenant, @customer, @project, @account, 'PADMIN1', '项目经理-测试',
   'seed-conv-9401', @zero_hash, '2026-08-04 11:00:00.000', @now, @now, 1);

INSERT INTO portal_project_messages
  (id, message_cursor, tenant_id, conversation_id, sender_type, sender_account_id, recipient_account_id,
   content, idempotency_key, request_hash, accepted_at)
VALUES
  (9501, 'cursor-test-9501', @tenant, 9401, 'CUSTOMER', @account, 'PADMIN1', '请问测评进度如何？', 'seed-msg-9501', @zero_hash, '2026-08-04 10:30:00.000'),
  (9502, 'cursor-test-9502', @tenant, 9401, 'MANAGER', 'PADMIN1', @account, '已进入评估阶段，预计本周完成。', 'seed-msg-9502', @zero_hash, '2026-08-04 10:45:00.000'),
  (9503, 'cursor-test-9503', @tenant, 9401, 'CUSTOMER', @account, 'PADMIN1', '好的，辛苦了。', 'seed-msg-9503', @zero_hash, '2026-08-04 11:00:00.000');

INSERT INTO portal_project_message_reads
  (id, tenant_id, conversation_id, message_id, reader_type, reader_account_id, read_at, created_at)
VALUES
  (9601, @tenant, 9401, 9502, 'CUSTOMER', @account, '2026-08-04 10:50:00.000', @now),
  (9602, @tenant, 9401, 9503, 'MANAGER', 'PADMIN1', '2026-08-04 11:05:00.000', @now);

INSERT INTO portal_project_message_read_receipts
  (id, tenant_id, conversation_id, reader_type, reader_account_id, last_read_message_id, last_read_cursor,
   read_at, created_at, updated_at, version)
VALUES
  (9701, @tenant, 9401, 'CUSTOMER', @account, 9502, 'cursor-test-9502', '2026-08-04 10:50:00.000', @now, @now, 1),
  (9702, @tenant, 9401, 'MANAGER', 'PADMIN1', 9503, 'cursor-test-9503', '2026-08-04 11:05:00.000', @now, @now, 1);

-- ============ 项目导出 ============
INSERT INTO portal_project_exports
  (id, public_id, tenant_id, customer_id, account_id, project_id, idempotency_key, request_hash,
   snapshot_json, source_updated_at, status, file_name, file_hash, file_size, file_bytes,
   failure_code, locked_by, locked_until, created_at, updated_at, completed_at, version)
VALUES
  (9801, 'exp-test-0000000001', @tenant, @customer, @account, @project, 'seed-export-9801', @zero_hash,
   JSON_OBJECT('project_id', @project, 'progress', 60), '2026-08-04 09:00:00.000', 'READY',
   'project-2026-08-04.pdf', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 204800, NULL,
   '', '', NULL, @now, @now, '2026-08-04 09:05:00.000', 1);

INSERT INTO portal_project_export_grants
  (id, public_id, tenant_id, customer_id, account_id, export_id, token_hash, status, expires_at, created_at, used_at)
VALUES
  (9901, 'grant-test-0000001', @tenant, @customer, @account, 9801, 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd', 'ACTIVE', '2026-09-01 00:00:00.000', @now, NULL);

INSERT INTO portal_project_export_events
  (id, tenant_id, customer_id, account_id, export_id, event_type, result, reason_code, request_trace, occurred_at)
VALUES
  (10001, @tenant, @customer, @account, 9801, 'EXPORT_CREATED', 'SUCCESS', '', 'trace-exp-9801', '2026-08-04 09:05:00.000');

-- ============ 报告 ============
INSERT INTO portal_report_requests
  (id, tenant_id, request_no, project_id, customer_id, account_id, report_type, reason, receive_email_cipher,
   status, downstream_request_id, approval_result, submitted_at, approved_at, issued_at, idempotency_key,
   request_hash, last_callback_version, last_callback_key, last_callback_hash, created_by, updated_by,
   created_at, updated_at, deleted_at, version)
VALUES
  (10101, @tenant, 'RP-TEST-20260804001', @project, @customer, @account, 'SECURITY_REPORT', '需要正式安全报告用于备案',
   @object_cipher, 'ISSUED', 'downstream-10101', '已批准', '2026-08-02 09:00:00.000', '2026-08-02 10:00:00.000', '2026-08-04 09:00:00.000',
   'seed-report-10101', @zero_hash, 1, 'cb-10101', @zero_hash, @account, @account, @now, @now, NULL, 1);

INSERT INTO portal_report_files
  (id, tenant_id, request_id, object_key_cipher, object_version, file_name, mime, size, file_hash,
   encryption_key_ref, encryption_algorithm, scan_status, scan_reference, scanned_at, watermark_status,
   created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (10201, @tenant, 10101, @object_cipher, 'v1', '安全测评报告.pdf', 'application/pdf', 1024000,
   'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'key-ref-10201', 'AES-256-GCM',
   'CLEAN', 'scan-ref-10201', '2026-08-04 08:30:00.000', 'WATERMARKED', @account, @account, @now, @now, NULL, 1);

INSERT INTO portal_report_grants
  (id, tenant_id, public_id, customer_id, request_id, account_id, token_hash, issue_key_hash, status,
   active_slot, expires_at, download_count, risk_state, last_download_at, created_by, updated_by,
   created_at, updated_at, deleted_at, version)
VALUES
  (10301, @tenant, 'rpt-grant-test-0001', @customer, 10101, @account,
   'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
   '1111111111111111111111111111111111111111111111111111111111111111',
   'ACTIVE', 'ACTIVE', '2026-09-01 00:00:00.000', 0, '', NULL, @account, @account, @now, @now, NULL, 1);

INSERT INTO portal_report_notifications
  (id, tenant_id, customer_id, request_id, account_id, kind, status, created_at, read_at)
VALUES
  (10401, @tenant, @customer, 10101, @account, 'REPORT_ISSUED', 'UNREAD', '2026-08-04 09:00:00.000', NULL);

INSERT INTO portal_report_status_events
  (id, tenant_id, customer_id, request_id, event_type, sequence, from_status, to_status, actor_type, actor_id,
   source_key_hash, payload_hash, request_trace, occurred_at)
VALUES
  (10501, @tenant, @customer, 10101, 'REPORT_SUBMITTED', 1, '', 'SUBMITTED', 'CUSTOMER', @account,
   @zero_hash, @zero_hash, 'trace-report-10101', '2026-08-02 09:00:00.000'),
  (10502, @tenant, @customer, 10101, 'REPORT_ISSUED', 2, 'APPROVED_PROCESSING', 'ISSUED', 'SYSTEM', 'portal-file-ingestor',
   '2222222222222222222222222222222222222222222222222222222222222222', @zero_hash, 'trace-report-10101', '2026-08-04 09:00:00.000');

INSERT INTO portal_report_risk_alerts
  (id, public_id, tenant_id, customer_id, request_id, grant_id, account_id, risk_code, status, active_slot,
   detected_at, acknowledged_at, resolved_at, resolved_by, resolution_action, resolution_reason, request_trace, version)
VALUES
  (10601, 'risk-alert-test-0001', @tenant, @customer, 10101, 10301, @account, 'RAPID_MULTI_DOWNLOAD', 'OPEN', 'OPEN',
   '2026-08-04 12:00:00.000', NULL, NULL, '', '', '', 'trace-risk-10601', 1);

-- ============ 服务评价 ============
INSERT INTO portal_service_evaluations
  (id, tenant_id, public_id, evaluation_no, customer_id, account_id, project_id, professional_score,
   response_score, report_score, attitude_score, total_score, average_score, comment, status, submitted_at,
   create_idempotency_key, create_request_hash, created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (10701, @tenant, 'eval-test-0001', 'EV-TEST-20260804001', @customer, @account, @project, 5, 4, 5, 5, 19, 4.75,
   '整体专业，响应及时。', 'SUBMITTED', '2026-08-04 16:00:00.000', 'seed-eval-10701', @zero_hash, @account, @account, @now, @now, NULL, 1);

-- ============ 客户反馈 ============
INSERT INTO portal_feedbacks
  (id, tenant_id, public_id, feedback_no, customer_id, account_id, project_id, type, title, description,
   expected_contact_cipher, expected_contact_masked, status, reject_reason, submitted_at, first_response_due_at,
   first_responded_at, resolved_at, closed_at, create_idempotency_key, create_request_hash, created_by, updated_by,
   created_at, updated_at, deleted_at, version)
VALUES
  (10801, @tenant, 'feedback-test-0001', 'FB-TEST-20260804001', @customer, @account, @project, 'SUGGESTION',
   '希望增加月度安全报告', '建议提供月度汇总报告。', @object_cipher, '138****6789', 'PROCESSING', '',
   '2026-08-04 13:00:00.000', '2026-08-05 13:00:00.000', '2026-08-04 14:00:00.000', NULL, NULL,
   'seed-feedback-10801', @zero_hash, @account, @account, @now, @now, NULL, 1);

INSERT INTO portal_feedback_messages
  (id, tenant_id, feedback_id, sender_type, sender_id, content, visibility, idempotency_key, request_hash, created_at)
VALUES
  (10901, @tenant, 10801, 'CUSTOMER', @account, '建议增加月度安全报告。', 'CUSTOMER', 'seed-fb-msg-10901', @zero_hash, '2026-08-04 13:00:00.000'),
  (10902, @tenant, 10801, 'OPERATOR', 'PADMIN1', '已受理，纳入月度运营计划。', 'CUSTOMER', 'seed-fb-msg-10902', @zero_hash, '2026-08-04 14:00:00.000');

INSERT INTO portal_feedback_status_logs
  (id, tenant_id, feedback_id, from_status, to_status, reason, actor_type, actor_id, request_id,
   idempotency_key, request_hash, occurred_at)
VALUES
  (11001, @tenant, 10801, '', 'SUBMITTED', '提交', 'CUSTOMER', @account, 'req-fb-10801', 'seed-fb-status-11001', @zero_hash, '2026-08-04 13:00:00.000'),
  (11002, @tenant, 10801, 'SUBMITTED', 'PROCESSING', '受理', 'OPERATOR', 'PADMIN1', 'req-fb-10801', 'seed-fb-status-11002', @zero_hash, '2026-08-04 14:00:00.000');

INSERT INTO portal_feedback_notifications
  (id, tenant_id, feedback_id, kind, status, created_at, read_at)
VALUES
  (11101, @tenant, 10801, 'NEW_FEEDBACK', 'UNREAD', '2026-08-04 13:00:00.000', NULL);

-- ============ 备案（备案表） ============
INSERT INTO portal_filings
  (id, tenant_id, public_id, filing_no, customer_id, account_id, project_id, form_version, status,
   current_step, completion_pct, submitted_at, locked_at, unlocked_at, unlock_reason_cipher,
   create_idempotency_key, create_request_hash, created_by, updated_by, created_at, updated_at, deleted_at, version)
VALUES
  (2, @tenant, 'filing-test-00000002', 'FIL-TEST-20260804002', @customer, @account, @project, '2025.1', 'DRAFT',
   2, 30, NULL, NULL, NULL, NULL, 'seed-filing-2', @zero_hash, @account, @account, @now, @now, NULL, 1);

INSERT INTO portal_filing_sections
  (id, tenant_id, filing_id, section_code, schema_version, data_cipher, validation_status, updated_by, created_at, updated_at, version)
VALUES
  (11201, @tenant, 2, 'ORGANIZATION', '2025.1', X'0102030405', 'VALID', @account, @now, @now, 1),
  (11202, @tenant, 2, 'CLASSIFIED_OBJECT', '2025.1', X'0102030405', 'INVALID', @account, @now, @now, 1);

INSERT INTO portal_filing_matrix
  (id, tenant_id, filing_id, matrix_code, row_code, column_code, selected, updated_by, created_at, updated_at, version)
VALUES
  (11301, @tenant, 2, 'BUSINESS_INFORMATION', '', '', 0, @account, @now, @now, 1),
  (11302, @tenant, 2, 'SYSTEM_SERVICE', 'LEGAL_RIGHTS', 'GENERAL_DAMAGE', 1, @account, @now, @now, 1);

INSERT INTO portal_filing_materials
  (id, tenant_id, public_id, filing_id, material_code, object_key_cipher, object_version, file_name, mime_type,
   size_bytes, sha256, scan_status, scan_reference, finalize_lease_until, create_actor_id, create_key_hash,
   create_request_hash, uploaded_at, scanned_at, created_by, updated_by, created_at, updated_at, version)
VALUES
  (11401, @tenant, 'mat-test-00000001', 2, 'NETWORK_TOPOLOGY', @object_cipher, 'v1', '网络拓扑.pdf', 'application/pdf',
   204800, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'CLEAN', 'scan-ref-mat-11401', NULL,
   @account, @zero_hash, @zero_hash, '2026-08-04 10:00:00.000', '2026-08-04 10:05:00.000', @account, @account, @now, @now, 1);

SELECT CONCAT('Portal test data imported: projects=', (SELECT COUNT(*) FROM portal_project_snapshots WHERE project_id=@project),
  ', reports=', (SELECT COUNT(*) FROM portal_report_requests WHERE id=10101),
  ', feedbacks=', (SELECT COUNT(*) FROM portal_feedbacks WHERE id=10801),
  ', filings=', (SELECT COUNT(*) FROM portal_filings WHERE id=2)) AS summary;
