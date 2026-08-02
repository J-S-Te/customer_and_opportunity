-- customer_portal schema only. These tables contain immutable security and
-- submission evidence. Production rollback must use a forward repair after an
-- approved retention/export review; this down file is for empty test schemas.
DROP TABLE IF EXISTS portal_filing_submission_outbox;
DROP TABLE IF EXISTS portal_filing_materials;
