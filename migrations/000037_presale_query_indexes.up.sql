-- CRM schema only. TS-007 expected-end/overdue filters are tenant-scoped and
-- cannot use the opportunity-prefixed reporting index from 000021 when no
-- opportunity filter is supplied.
ALTER TABLE crm_presale_requests
  ADD KEY idx_presale_expected_status (tenant_id,expected_end,status,id);

-- No row backfill is required. On a populated request table, execute through
-- the release platform's online-DDL procedure and monitor metadata locks,
-- replica lag and temporary disk usage before enabling the new query traffic.
