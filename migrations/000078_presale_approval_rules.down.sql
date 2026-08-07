DROP TABLE IF EXISTS crm_presale_approval_rules;
ALTER TABLE crm_presale_approval_instances
  DROP COLUMN nodes_json,
  DROP COLUMN rule_version,
  DROP COLUMN rule_id;
ALTER TABLE crm_presale_requests
  DROP COLUMN execution_department,
  DROP COLUMN execution_department_id;
