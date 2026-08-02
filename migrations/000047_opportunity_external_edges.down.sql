-- Destructive rollback is safe only before external snapshots or signed
-- transfer events are used. In production preserve evidence and use forward
-- repair; OPPORTUNITY_SIGNED outbox rows are intentionally not deleted here.
DROP TABLE IF EXISTS crm_opportunity_external_links;
