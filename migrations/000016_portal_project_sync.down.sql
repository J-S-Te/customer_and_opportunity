-- Controlled empty-environment rollback only. In production prefer a forward
-- repair; dropping cursor/success/error state destroys synchronization evidence.
DROP TABLE IF EXISTS portal_project_sync_states;

ALTER TABLE portal_project_snapshots
  CHANGE COLUMN manager_name_snapshot manager_name VARCHAR(128) NOT NULL DEFAULT '';
