-- Target schema: CRM. 回退前必须确认不存在长度超过 191 的 source_event_id。
ALTER TABLE crm_notifications MODIFY COLUMN source_event_id VARCHAR(191) NOT NULL;
