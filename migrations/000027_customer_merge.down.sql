-- Destructive rollback is allowed only before customer merge traffic. After a
-- merge, these append-only records are required to explain the mapping and a
-- production rollback must use a forward migration instead.
DROP TABLE IF EXISTS crm_customer_merge_idempotency;
DROP TABLE IF EXISTS crm_customer_merge_logs;
