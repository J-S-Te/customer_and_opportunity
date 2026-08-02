-- Safe only before any opportunity has been updated or voided with the new release.
DROP INDEX idx_opportunity_lifecycle ON crm_opportunities;
ALTER TABLE crm_opportunities
  DROP COLUMN status_before_void,
  DROP COLUMN end_date,
  DROP COLUMN competitor_info,
  DROP COLUMN pain_points,
  DROP COLUMN system_count;
