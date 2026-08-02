package presalereportaggregateworker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const aggregateLeaseName = "presale-report-daily-aggregate"

var errLeaseLost = errors.New("presale report aggregate lease was lost")

type App struct {
	db            *gorm.DB
	workerID      string
	pollInterval  time.Duration
	leaseDuration time.Duration
	lookbackDays  int
	tenantIDs     []string
	now           func() time.Time
}

// RunOnce performs one bounded rebuild and is intended for release checks,
// controlled backfills and tests. Normal deployments should call Run.
func (a *App) RunOnce(ctx context.Context) error { return a.runOnce(ctx) }

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	return newApp(db, config), nil
}

func newApp(db *gorm.DB, config Config) *App {
	return &App{
		db: db, workerID: config.WorkerID, pollInterval: config.PollInterval,
		leaseDuration: config.LeaseDuration, lookbackDays: config.LookbackDays,
		tenantIDs: append([]string(nil), config.TenantIDs...),
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (a *App) Close() error {
	db, err := a.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (a *App) Run(ctx context.Context) error {
	if err := a.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("presale report aggregate failed: %v", err)
			}
		}
	}
}

func (a *App) runOnce(ctx context.Context) error {
	now := a.now().UTC()
	from, to := aggregateWindow(now, a.lookbackDays)
	acquired, leaseUntil, err := a.acquireLease(ctx, now)
	if err != nil || !acquired {
		return err
	}
	tenants := append([]string(nil), a.tenantIDs...)
	if len(tenants) == 0 {
		tenants, err = a.discoverTenants(ctx, from, to)
		if err != nil {
			return err
		}
	}
	for _, tenantID := range tenants {
		if err = validateTenantID(tenantID); err != nil {
			return err
		}
		leaseUntil, err = a.renewLease(ctx, a.now().UTC())
		if err != nil {
			return err
		}
		deadline := leaseUntil.Add(-time.Second)
		if !deadline.After(a.now().UTC()) {
			return errLeaseLost
		}
		tenantCtx, cancel := context.WithDeadline(ctx, deadline)
		err = a.aggregateTenant(tenantCtx, tenantID, from, to)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func aggregateWindow(now time.Time, lookbackDays int) (time.Time, time.Time) {
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -(lookbackDays - 1)), day.AddDate(0, 0, 1)
}

func validateTenantID(value string) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || len(value) > 64 {
		return fmt.Errorf("invalid aggregate tenant identifier")
	}
	return nil
}

func (a *App) discoverTenants(ctx context.Context, from, to time.Time) ([]string, error) {
	var tenants []string
	err := a.db.WithContext(ctx).Raw(`SELECT tenant_id FROM (
		SELECT DISTINCT tenant_id FROM crm_presale_worklogs
		 WHERE work_start>=? AND work_start<?
		UNION
		SELECT DISTINCT tenant_id FROM crm_presale_daily_metrics
		 WHERE metric_date>=DATE(?) AND metric_date<DATE(?)
	) candidates ORDER BY tenant_id`, from, to, from, to).Scan(&tenants).Error
	return tenants, err
}

func (a *App) acquireLease(ctx context.Context, now time.Time) (bool, time.Time, error) {
	var acquired bool
	leaseUntil := now.Add(a.leaseDuration)
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT IGNORE INTO crm_presale_job_leases(job_name,owner_id,lease_until,updated_at) VALUES(?,?,?,?)`, aggregateLeaseName, a.workerID, leaseUntil, now).Error; err != nil {
			return err
		}
		var lease struct {
			OwnerID    string
			LeaseUntil time.Time
		}
		if err := tx.Table("crm_presale_job_leases").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("owner_id,lease_until").Where("job_name=?", aggregateLeaseName).Take(&lease).Error; err != nil {
			return err
		}
		if lease.OwnerID != a.workerID && lease.LeaseUntil.After(now) {
			return nil
		}
		result := tx.Table("crm_presale_job_leases").Where("job_name=?", aggregateLeaseName).
			Updates(map[string]any{"owner_id": a.workerID, "lease_until": leaseUntil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		// INSERT IGNORE may have inserted this exact lease in the same
		// transaction. MySQL then reports zero changed rows for the identical
		// UPDATE; the locked row and owner/expiry check above are authoritative.
		acquired = true
		return nil
	})
	return acquired, leaseUntil, err
}

func (a *App) renewLease(ctx context.Context, now time.Time) (time.Time, error) {
	leaseUntil := now.Add(a.leaseDuration)
	result := a.db.WithContext(ctx).Exec(leaseRenewSQL(), leaseUntil, now, aggregateLeaseName, a.workerID, now)
	if result.Error != nil {
		return time.Time{}, result.Error
	}
	if result.RowsAffected != 1 {
		return time.Time{}, errLeaseLost
	}
	return leaseUntil, nil
}

func leaseRenewSQL() string {
	return `UPDATE crm_presale_job_leases SET lease_until=?,updated_at=?
		WHERE job_name=? AND owner_id=? AND lease_until>=?`
}

func (a *App) aggregateTenant(ctx context.Context, tenantID string, from, to time.Time) error {
	startedAt := a.now().UTC()
	run := dailyMetricRun{
		TenantID: tenantID, WindowStart: from, WindowEndExclusive: to,
		Status: "RUNNING", WorkerID: a.workerID, StartedAt: startedAt,
	}
	if err := a.db.WithContext(ctx).Table("crm_presale_daily_metric_runs").Create(&run).Error; err != nil {
		return err
	}
	var rowCount int64
	var sourceMax *time.Time
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM crm_presale_daily_metrics
			WHERE tenant_id=? AND metric_date>=DATE(?) AND metric_date<DATE(?)`, tenantID, from, to).Error; err != nil {
			return err
		}
		result := tx.Exec(dailyMetricInsertSQL(), startedAt, tenantID, from, to)
		if result.Error != nil {
			return result.Error
		}
		rowCount = result.RowsAffected
		if err := tx.Table("crm_presale_daily_metrics").Select("MAX(source_max_updated_at)").
			Where("tenant_id=? AND metric_date>=DATE(?) AND metric_date<DATE(?)", tenantID, from, to).
			Scan(&sourceMax).Error; err != nil {
			return err
		}
		finishedAt := a.now().UTC()
		result = tx.Table("crm_presale_daily_metric_runs").Where("id=? AND tenant_id=? AND status='RUNNING'", run.ID, tenantID).
			Updates(map[string]any{"status": "SUCCESS", "row_count": rowCount, "source_max_updated_at": sourceMax, "finished_at": finishedAt})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("daily aggregate run state changed concurrently")
		}
		return nil
	})
	if err == nil {
		return nil
	}
	finishedAt := a.now().UTC()
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	updateErr := a.db.WithContext(recoveryCtx).Table("crm_presale_daily_metric_runs").
		Where("id=? AND tenant_id=? AND status='RUNNING'", run.ID, tenantID).
		Updates(map[string]any{"status": "FAILED", "finished_at": finishedAt, "error_summary": sanitize(err.Error())}).Error
	if updateErr != nil {
		return errors.Join(err, updateErr)
	}
	return err
}

type dailyMetricRun struct {
	ID                 uint64
	TenantID           string
	WindowStart        time.Time
	WindowEndExclusive time.Time
	Status             string
	WorkerID           string
	RowCount           uint64
	SourceMaxUpdatedAt *time.Time
	StartedAt          time.Time
	FinishedAt         *time.Time
	ErrorSummary       string
}

func dailyMetricInsertSQL() string {
	return `INSERT INTO crm_presale_daily_metrics
		(tenant_id,metric_date,organization_id,person_id,person_name_snapshot,department_snapshot,
		 opportunity_id,work_hours,request_count,worklog_count,pms_outbox_worklog_count,
		 pms_success_count,source_max_updated_at,aggregated_at)
	SELECT aggregate_rows.tenant_id,aggregate_rows.metric_date,aggregate_rows.organization_id,
	       aggregate_rows.person_id,snapshot.person_name_snapshot,snapshot.department_snapshot,
	       aggregate_rows.opportunity_id,aggregate_rows.work_hours,aggregate_rows.request_count,
	       aggregate_rows.worklog_count,aggregate_rows.pms_outbox_worklog_count,
		       aggregate_rows.pms_success_count,aggregate_rows.source_max_updated_at,?
	FROM (
		SELECT w.tenant_id,DATE(w.work_start) AS metric_date,o.owner_org_id AS organization_id,
		       w.person_id,o.id AS opportunity_id,MAX(w.id) AS snapshot_worklog_id,
		       SUM(w.work_hours) AS work_hours,COUNT(DISTINCT w.request_id) AS request_count,
		       COUNT(DISTINCT w.id) AS worklog_count,
		       SUM(CASE WHEN EXISTS (
		         SELECT 1 FROM crm_outbox_events e
		          WHERE e.tenant_id=w.tenant_id AND e.aggregate_type='presale_worklog'
		            AND e.aggregate_id=CAST(w.id AS CHAR) AND e.event_type='PRESALE_WORKLOG_CREATED'
		       ) THEN 1 ELSE 0 END) AS pms_outbox_worklog_count,
		       SUM(CASE WHEN w.push_status='SUCCESS' AND EXISTS (
		         SELECT 1 FROM crm_outbox_events e
		          WHERE e.tenant_id=w.tenant_id AND e.aggregate_type='presale_worklog'
		            AND e.aggregate_id=CAST(w.id AS CHAR) AND e.event_type='PRESALE_WORKLOG_CREATED'
		       ) THEN 1 ELSE 0 END) AS pms_success_count,
		       MAX(GREATEST(w.updated_at,r.updated_at,o.updated_at)) AS source_max_updated_at
		FROM crm_presale_worklogs w
		JOIN crm_presale_requests r ON r.tenant_id=w.tenant_id AND r.id=w.request_id
		JOIN crm_opportunities o ON o.tenant_id=r.tenant_id AND o.id=r.opportunity_id
		WHERE w.tenant_id=? AND w.deleted_at IS NULL AND w.voided_at IS NULL
		  AND w.work_start>=? AND w.work_start<?
		GROUP BY w.tenant_id,DATE(w.work_start),o.owner_org_id,w.person_id,o.id
	) aggregate_rows
	JOIN crm_presale_worklogs snapshot
	  ON snapshot.tenant_id=aggregate_rows.tenant_id AND snapshot.id=aggregate_rows.snapshot_worklog_id`
}

func sanitize(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " "))
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}
