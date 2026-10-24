-- CRM schema. TS-011 售前两级执行指派：
-- 技术总监先写执行部门与团队负责人，再由该负责人写入执行人。
-- 历史 EXECUTING / COMPLETED 申请不迁移、不回填，保持原状态与指派记录。

ALTER TABLE crm_presale_requests
  ADD COLUMN team_lead_person_id VARCHAR(64) NOT NULL DEFAULT '' AFTER execution_department,
  ADD COLUMN team_lead_name_snapshot VARCHAR(128) NOT NULL DEFAULT '' AFTER team_lead_person_id,
  ADD COLUMN assignment_level TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER team_lead_name_snapshot,
  ADD KEY idx_crm_presale_requests_team_lead (tenant_id, team_lead_person_id, status);

ALTER TABLE crm_presale_assignments
  ADD COLUMN assignment_kind VARCHAR(16) NOT NULL DEFAULT 'EXECUTOR' AFTER assignee_role,
  ADD KEY idx_crm_presale_assignments_kind_current (tenant_id, request_id, assignment_kind, is_current),
  ADD CONSTRAINT chk_crm_presale_assignment_kind CHECK (assignment_kind IN ('TEAM_LEAD','EXECUTOR'));

-- 新增操作会写入 crm_presale_mutation_replays；旧约束只允许审批/改派/取消，
-- 必须在同一迁移中扩展，否则两级指派在记录幂等结果时触发 CHECK 失败。
ALTER TABLE crm_presale_mutation_replays
  DROP CHECK chk_presale_mutation_operation_action,
  DROP CHECK chk_presale_mutation_action,
  DROP CHECK chk_presale_mutation_operation,
  ADD CONSTRAINT chk_presale_mutation_operation CHECK (operation IN (
    'APPROVAL_ACTION','REPLACE_ASSIGNMENTS','CANCEL',
    'SELECT_EXECUTION_DEPARTMENT','REPLACE_EXECUTOR_ASSIGNMENTS','REJECT_EXECUTION_ASSIGNMENT'
  )),
  ADD CONSTRAINT chk_presale_mutation_action CHECK (action IN (
    'NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT',
    'REPLACE','CANCEL','TEAM_LEAD','EXECUTOR','REJECT'
  )),
  ADD CONSTRAINT chk_presale_mutation_operation_action CHECK (
    (operation = 'APPROVAL_ACTION' AND action IN ('NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT')) OR
    (operation = 'REPLACE_ASSIGNMENTS' AND action = 'REPLACE') OR
    (operation = 'CANCEL' AND action = 'CANCEL') OR
    (operation = 'SELECT_EXECUTION_DEPARTMENT' AND action = 'TEAM_LEAD') OR
    (operation = 'REPLACE_EXECUTOR_ASSIGNMENTS' AND action = 'REPLACE') OR
    (operation = 'REJECT_EXECUTION_ASSIGNMENT' AND action = 'REJECT')
  );
