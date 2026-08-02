-- Target schema: CRM. Destructive: removes the independent membership-term
-- ledger. Use only in an empty/test environment or after exporting all terms.
DROP TABLE IF EXISTS crm_opportunity_member_terms;
ALTER TABLE crm_opportunity_members DROP INDEX uq_opportunity_member_tenant_id;
