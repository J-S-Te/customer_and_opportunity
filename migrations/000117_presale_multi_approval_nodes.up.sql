-- TS-011 follow-up: approval rules may contain up to ten APPROVAL nodes. Extend
-- the mutation-replay checks so the third and later approval decisions can be
-- recorded transactionally instead of failing after the workflow advances.
ALTER TABLE crm_presale_mutation_replays
  DROP CHECK chk_presale_mutation_operation_action,
  DROP CHECK chk_presale_mutation_action,
  ADD CONSTRAINT chk_presale_mutation_action CHECK (action IN (
    'NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT',
    'NODE_3_PASS','NODE_3_REJECT','NODE_4_PASS','NODE_4_REJECT',
    'NODE_5_PASS','NODE_5_REJECT','NODE_6_PASS','NODE_6_REJECT',
    'NODE_7_PASS','NODE_7_REJECT','NODE_8_PASS','NODE_8_REJECT',
    'NODE_9_PASS','NODE_9_REJECT','NODE_10_PASS','NODE_10_REJECT',
    'REPLACE','CANCEL','TEAM_LEAD','EXECUTOR','REJECT'
  )),
  ADD CONSTRAINT chk_presale_mutation_operation_action CHECK (
    (operation = 'APPROVAL_ACTION' AND action IN (
      'NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT',
      'NODE_3_PASS','NODE_3_REJECT','NODE_4_PASS','NODE_4_REJECT',
      'NODE_5_PASS','NODE_5_REJECT','NODE_6_PASS','NODE_6_REJECT',
      'NODE_7_PASS','NODE_7_REJECT','NODE_8_PASS','NODE_8_REJECT',
      'NODE_9_PASS','NODE_9_REJECT','NODE_10_PASS','NODE_10_REJECT'
    )) OR
    (operation = 'REPLACE_ASSIGNMENTS' AND action = 'REPLACE') OR
    (operation = 'CANCEL' AND action = 'CANCEL') OR
    (operation = 'SELECT_EXECUTION_DEPARTMENT' AND action = 'TEAM_LEAD') OR
    (operation = 'REPLACE_EXECUTOR_ASSIGNMENTS' AND action = 'REPLACE') OR
    (operation = 'REJECT_EXECUTION_ASSIGNMENT' AND action = 'REJECT')
  );
