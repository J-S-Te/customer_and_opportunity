DROP TABLE IF EXISTS crm_credit_rule_setting_versions;
ALTER TABLE crm_customer_credit_applications DROP COLUMN approval_instance_id;
DROP TABLE IF EXISTS crm_approval_tasks;
DROP TABLE IF EXISTS crm_approval_instances;
