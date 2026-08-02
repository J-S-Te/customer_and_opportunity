-- Destructive rollback is allowed only before any attachment metadata exists.
-- In production, disable the attachment routes/adapters and use a forward
-- migration; retaining object references and scan/audit evidence is required.
DROP TABLE IF EXISTS crm_opportunity_attachments;
