-- Target schema: customer_portal. Store only the validated base-platform login
-- IP in the server-side session; existing sessions retain an empty value.
ALTER TABLE portal_sessions
  ADD COLUMN login_ip VARCHAR(45) NOT NULL DEFAULT '' AFTER platform_user_id;
