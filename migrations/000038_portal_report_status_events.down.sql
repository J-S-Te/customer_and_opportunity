-- customer_portal schema only. Production rollback is not supported after
-- any event has been written: these rows are immutable audit records. Use
-- only for empty/test schemas after confirming the table is empty.
DROP TABLE portal_report_status_events;

ALTER TABLE portal_report_requests
  DROP KEY uq_portal_report_scope_id;
