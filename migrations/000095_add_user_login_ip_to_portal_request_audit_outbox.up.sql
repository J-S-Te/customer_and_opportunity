-- Target schema: customer_portal. Persist the optional, trusted base-platform
-- login IP separately from the current request peer address. Existing rows
-- remain compatible with ''.
ALTER TABLE portal_application_request_audit_outbox
  ADD COLUMN user_login_ip VARCHAR(45) NOT NULL DEFAULT '' AFTER actor_name;
