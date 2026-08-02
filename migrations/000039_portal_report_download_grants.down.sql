-- customer_portal schema only. Production rollback is not supported after
-- any grant or download event exists because the event table is immutable
-- audit evidence. Use only on an empty/test schema after explicitly confirming
-- both tables are empty and report download routes are disabled.
DROP TABLE portal_report_download_events;
DROP TABLE portal_report_grants;
