-- 站内信 source_event_id 现在绑定到具体收件人（tenant:type:request_no:recipient_id），
-- 以区分同一请求多级审批的“启动通知”与“流转通知”。原 191 长度不足以容纳收件人标识。
ALTER TABLE crm_notifications MODIFY COLUMN source_event_id VARCHAR(256) NOT NULL;
