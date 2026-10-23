-- CRM schema. 受控回退 TS-011 两级执行指派结构。
-- 回退会丢失申请上的团队负责人投影与指派类型语义；已有两级在途/历史申请时
-- 必须优先前向修复，不能把本文件当作常规生产回滚。

ALTER TABLE crm_presale_assignments
  DROP CHECK chk_crm_presale_assignment_kind,
  DROP KEY idx_crm_presale_assignments_kind_current,
  DROP COLUMN assignment_kind;

ALTER TABLE crm_presale_requests
  DROP KEY idx_crm_presale_requests_team_lead,
  DROP COLUMN assignment_level,
  DROP COLUMN team_lead_name_snapshot,
  DROP COLUMN team_lead_person_id;

-- 恢复 000040 的三条变更记录约束；若已存在两级指派操作行，恢复会失败关闭，
-- 需先导出/清理这些幂等记录并确认业务影响，不能静默丢弃。
ALTER TABLE crm_presale_mutation_replays
  DROP CHECK chk_presale_mutation_operation_action,
  DROP CHECK chk_presale_mutation_action,
  DROP CHECK chk_presale_mutation_operation,
  ADD CONSTRAINT chk_presale_mutation_operation CHECK (operation IN ('APPROVAL_ACTION','REPLACE_ASSIGNMENTS','CANCEL')),
  ADD CONSTRAINT chk_presale_mutation_action CHECK (action IN ('NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT','REPLACE','CANCEL')),
  ADD CONSTRAINT chk_presale_mutation_operation_action CHECK (
    (operation = 'APPROVAL_ACTION' AND action IN ('NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT')) OR
    (operation = 'REPLACE_ASSIGNMENTS' AND action = 'REPLACE') OR
    (operation = 'CANCEL' AND action = 'CANCEL')
  );
