-- Recovery is destructive and permitted only before exports exist. In production,
-- disable project.export, stop the render worker, and use a forward migration.
DROP TABLE IF EXISTS portal_project_export_events;
DROP TABLE IF EXISTS portal_project_export_grants;
DROP TABLE IF EXISTS portal_project_exports;
