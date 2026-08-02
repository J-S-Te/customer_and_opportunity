-- Target schema: CRM. TS-004 progress, immutable recipient evidence and one
-- opaque-reference outbox row per recipient are committed in one transaction.
ALTER TABLE crm_presale_progress_logs
  ADD UNIQUE KEY uq_presale_progress_tenant_id (tenant_id,id);

CREATE TABLE crm_presale_progress_notification_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id CHAR(64) NOT NULL, tenant_id VARCHAR(64) NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL, progress_id BIGINT UNSIGNED NOT NULL,
  assignment_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  recipient_id VARCHAR(64) NOT NULL, recipient_namespace VARCHAR(16) NOT NULL,
  recipient_kind VARCHAR(32) NOT NULL, author_user_id VARCHAR(64) NOT NULL,
  author_person_id VARCHAR(64) NOT NULL, request_id_trace VARCHAR(64) NOT NULL DEFAULT '',
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_progress_notification_event_id (event_id),
  KEY idx_presale_progress_notification_request (tenant_id,request_id,occurred_at,id),
  KEY idx_presale_progress_notification_recipient (tenant_id,recipient_namespace,recipient_id,occurred_at,id),
  CONSTRAINT chk_presale_progress_notification_namespace CHECK (recipient_namespace IN ('USER','PERSON')),
  CONSTRAINT chk_presale_progress_notification_kind CHECK (recipient_kind IN ('APPLICANT','CURRENT_ASSIGNEE')),
  CONSTRAINT chk_presale_progress_notification_shape CHECK (
    (recipient_namespace='USER' AND recipient_kind='APPLICANT' AND assignment_id=0) OR
    (recipient_namespace='PERSON' AND recipient_kind='CURRENT_ASSIGNEE' AND assignment_id>0)
  ),
  CONSTRAINT fk_presale_progress_notification_request FOREIGN KEY (tenant_id,request_id)
    REFERENCES crm_presale_requests(tenant_id,id),
  CONSTRAINT fk_presale_progress_notification_progress FOREIGN KEY (tenant_id,progress_id)
    REFERENCES crm_presale_progress_logs(tenant_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE crm_notifications
  ADD COLUMN progress_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER assignment_id,
  ADD KEY idx_crm_notification_progress (tenant_id,progress_id,created_at),
  DROP CHECK chk_crm_notification_recipient_kind,
  DROP CHECK chk_crm_notification_resource_shape,
  ADD CONSTRAINT chk_crm_notification_recipient_kind CHECK (
    recipient_kind IN ('PREVIOUS_OWNER','NEW_OWNER','ASSIGNEE_ADDED','ASSIGNEE_REMOVED','PROGRESS_APPLICANT','PROGRESS_ASSIGNEE')
  ),
  ADD CONSTRAINT chk_crm_notification_resource_shape CHECK (
    (type='OPPORTUNITY_OWNER_CHANGED' AND opportunity_id>0 AND request_id=0 AND request_no='' AND assignment_id=0 AND progress_id=0) OR
    (type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED') AND request_id>0 AND request_no<>'' AND assignment_id>0 AND progress_id=0) OR
    (type='PRESALE_PROGRESS_APPLICANT' AND request_id>0 AND request_no<>'' AND assignment_id=0 AND progress_id>0) OR
    (type='PRESALE_PROGRESS_ASSIGNEE' AND request_id>0 AND request_no<>'' AND assignment_id>0 AND progress_id>0)
  );

-- No historical notifications are synthesized: old progress rows do not have
-- an original recipient-set snapshot or stable event identity. Both ALTERs may
-- require metadata locks on populated tables and need the release platform's
-- approved online-schema-change procedure.
