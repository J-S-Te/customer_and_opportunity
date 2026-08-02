DROP TABLE IF EXISTS portal_report_outbox;
DROP TABLE IF EXISTS portal_report_files;
DROP TABLE IF EXISTS portal_report_requests;
DROP TABLE IF EXISTS portal_project_team;
DROP TABLE IF EXISTS portal_project_activities;
DROP TABLE IF EXISTS portal_project_milestones;
DROP TABLE IF EXISTS portal_project_snapshots;
DROP TABLE IF EXISTS portal_auth_events;
DROP TABLE IF EXISTS portal_activation_contexts;
DROP TABLE IF EXISTS portal_sessions;
DROP TABLE IF EXISTS portal_identity_links;

-- Production recovery must export newly created Portal business records before
-- this destructive down migration. Prefer forward repair after live traffic.
