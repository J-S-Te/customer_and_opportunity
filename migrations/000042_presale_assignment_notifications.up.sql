-- CRM schema only. Run after 000040. Assignment-set mutations, immutable
-- evidence and notification outbox rows commit in one transaction.
ALTER TABLE crm_presale_assignments
  ADD UNIQUE KEY uq_presale_assignment_tenant_id (tenant_id,id);

CREATE TABLE crm_presale_assignment_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id CHAR(64) NOT NULL, tenant_id VARCHAR(64) NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL, assignment_id BIGINT UNSIGNED NOT NULL,
  event_type VARCHAR(16) NOT NULL, recipient_person_id VARCHAR(64) NOT NULL,
  person_name_snapshot VARCHAR(128) NOT NULL, role_snapshot VARCHAR(32) NOT NULL,
  change_reason VARCHAR(1000) NOT NULL, actor_id VARCHAR(64) NOT NULL,
  request_id_trace VARCHAR(64) NOT NULL DEFAULT '', occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_assignment_event_id (event_id),
  KEY idx_presale_assignment_event_request (tenant_id,request_id,occurred_at,id),
  KEY idx_presale_assignment_event_recipient (tenant_id,recipient_person_id,occurred_at,id),
  CONSTRAINT chk_presale_assignment_event_type CHECK (event_type IN ('ADDED','REMOVED')),
  CONSTRAINT fk_presale_assignment_event_request FOREIGN KEY (tenant_id,request_id)
    REFERENCES crm_presale_requests(tenant_id,id),
  CONSTRAINT fk_presale_assignment_event_assignment FOREIGN KEY (tenant_id,assignment_id)
    REFERENCES crm_presale_assignments(tenant_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE crm_notifications
  ADD COLUMN request_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER opportunity_name,
  ADD COLUMN request_no VARCHAR(32) NOT NULL DEFAULT '' AFTER request_id,
  ADD COLUMN assignment_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER request_no,
  ADD KEY idx_crm_notification_request (tenant_id,request_id,created_at),
  DROP CHECK chk_crm_notification_recipient_kind,
  ADD CONSTRAINT chk_crm_notification_recipient_kind CHECK (
    recipient_kind IN ('PREVIOUS_OWNER','NEW_OWNER','ASSIGNEE_ADDED','ASSIGNEE_REMOVED')
  ),
  ADD CONSTRAINT chk_crm_notification_resource_shape CHECK (
    (type = 'OPPORTUNITY_OWNER_CHANGED' AND opportunity_id > 0 AND request_id = 0 AND request_no = '' AND assignment_id = 0) OR
    (type IN ('PRESALE_ASSIGNEE_ADDED','PRESALE_ASSIGNEE_REMOVED') AND request_id > 0 AND request_no <> '' AND assignment_id > 0)
  );

-- No historical assignment notifications are synthesized because prior
-- commands lack a trustworthy original notification event identity. Existing
-- opportunity notification rows receive only the neutral 0/empty defaults.
-- The new event table is append-only. The assignment unique index and the
-- crm_notifications ALTER may take metadata locks; production must use the
-- release platform's approved online-schema-change process and monitor lag.
