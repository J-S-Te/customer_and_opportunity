-- Display only; existing sessions remain valid and require a fresh login to obtain username.
ALTER TABLE portal_sessions ADD COLUMN username VARCHAR(255) NOT NULL DEFAULT '';
