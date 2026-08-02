-- Target schema: CRM. Destructive rollback is allowed only in an empty/test
-- environment or after exporting all historical member rows and audit/outbox
-- references. Production rollback should disable the UI and use forward repair.
DROP TABLE IF EXISTS crm_opportunity_change_idempotency;
DROP TABLE IF EXISTS crm_opportunity_members;
