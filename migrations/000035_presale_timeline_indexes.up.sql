-- CRM schema only. Cover the immutable TS sources used by the descending
-- (occurred_at,type_priority,source_id) timeline keyset query.
ALTER TABLE crm_presale_assignments
  ADD KEY idx_presale_assignment_end_timeline (tenant_id,request_id,ended_at,id);

ALTER TABLE crm_presale_worklogs
  ADD KEY idx_presale_worklog_timeline (tenant_id,request_id,created_at,id);

-- No row backfill is required: the endpoint reads the existing append-only
-- status/approval/progress sources plus immutable assignment/worklog facts.
-- Assignment-created events reuse idx_presale_assignment_request from 000006;
-- InnoDB secondary indexes carry the primary-key ID as their final key part.
-- Production execution on populated tables must use the release platform's
-- online-DDL procedure and monitor metadata locks and replica lag.
