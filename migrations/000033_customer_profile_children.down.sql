-- Empty-environment rollback only. In production these customer records are
-- retained for at least six years; back up and use a forward fix instead.
DROP TABLE IF EXISTS crm_customer_systems;
DROP TABLE IF EXISTS crm_customer_stakeholders;
ALTER TABLE crm_customers DROP KEY uq_customer_tenant_id;
