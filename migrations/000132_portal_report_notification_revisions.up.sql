-- Portal report notifications are unique per issued revision, preserving old notice IDs,
-- read state and append-only read evidence. Apply with Portal writers stopped.
-- Existing issuers wrote the file and notification with the same transaction timestamp.
-- A missing or ambiguous exact file match requires manual evidence review before upgrade;
-- never infer a revision from current_report_revision or from the oldest/newest file.
-- The temporary guard fails before persistent DDL or notification updates.
CREATE TEMPORARY TABLE portal_report_notice_revision_guard (
  candidate_count BIGINT UNSIGNED NOT NULL,
  CONSTRAINT chk_portal_notice_revision_requires_manual_review CHECK (candidate_count = 1)
);

INSERT INTO portal_report_notice_revision_guard (candidate_count)
SELECT COUNT(f.id)
FROM portal_report_notifications AS n
LEFT JOIN portal_report_files AS f
  ON f.tenant_id = n.tenant_id
  AND f.request_id = n.request_id
  AND f.created_at = n.created_at
WHERE n.kind = 'REPORT_ISSUED'
GROUP BY n.id
HAVING COUNT(f.id) <> 1;

DROP TEMPORARY TABLE portal_report_notice_revision_guard;

ALTER TABLE portal_report_notifications
  ADD COLUMN report_revision INT UNSIGNED NOT NULL DEFAULT 0 AFTER request_id;

UPDATE portal_report_notifications AS n
JOIN portal_report_files AS f
  ON f.tenant_id = n.tenant_id
  AND f.request_id = n.request_id
  AND f.created_at = n.created_at
SET n.report_revision = f.report_revision
WHERE n.kind = 'REPORT_ISSUED';

-- Submission failures occur before any issued revision and retain revision zero.
-- Keep the scope index referenced by notification_read_events unchanged.
ALTER TABLE portal_report_notifications
  DROP INDEX uq_portal_report_notification_kind,
  ADD UNIQUE KEY uq_portal_report_notification_kind
    (tenant_id,customer_id,request_id,account_id,kind,report_revision),
  DROP CHECK chk_portal_report_notification_kind,
  ADD CONSTRAINT chk_portal_report_notification_kind
    CHECK (kind IN ('REPORT_ISSUED','REPORT_SUBMISSION_FAILED'));
