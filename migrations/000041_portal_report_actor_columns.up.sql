-- customer_portal schema only. OIDC subject/account identifiers are allowed up
-- to 128 bytes by the Portal identity mapping and report domain. Widen only the
-- report audit columns that previously inherited the shared 64-byte default.
ALTER TABLE portal_report_requests
  MODIFY COLUMN created_by VARCHAR(128) NOT NULL,
  MODIFY COLUMN updated_by VARCHAR(128) NOT NULL;

ALTER TABLE portal_report_grants
  MODIFY COLUMN created_by VARCHAR(128) NOT NULL,
  MODIFY COLUMN updated_by VARCHAR(128) NOT NULL;
