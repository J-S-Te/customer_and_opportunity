package opportunityalertworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	leaseName               = "opportunity-stage-alert-scan"
	stageAlertEventType     = "OPPORTUNITY_STAGE_ALERT_SITE_MESSAGE"
	stageAlertAggregateType = "opportunity_stage_alert"
	workerActor             = "opportunity-alert-worker"
	statusFollowing         = "FOLLOWING"
	statusOutboxPending     = "PENDING"
	statusOutboxSent        = "SENT"
	statusOutboxCancelled   = "CANCELLED"
)

var errLeaseLost = errors.New("opportunity stage alert scanner lease lost")

type App struct {
	db            *gorm.DB
	workerID      string
	pollInterval  time.Duration
	leaseDuration time.Duration
	batchSize     int
	now           func() time.Time
}

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	return &App{
		db: db, workerID: config.WorkerID, pollInterval: config.PollInterval,
		leaseDuration: config.LeaseDuration, batchSize: config.BatchSize,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (a *App) Close() error {
	db, err := a.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (a *App) Run(ctx context.Context) error {
	if err := a.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

type scanOpportunity struct {
	ID             uint64
	TenantID       string
	OpportunityNo  string
	Name           string
	OwnerUserID    string
	CurrentStage   string
	Status         string
	StageChangedAt time.Time
}

type stageRule struct {
	Stage          string
	ThresholdHours uint32
	ConfigVersion  uint64
}

type alertIdentity struct {
	Stage            string
	ThresholdVersion uint64
	RecipientID      string
}

// Scan owns the tenant-independent scanner lease for one cycle. Candidate
// pagination is deterministic and every decision is repeated while the target
// opportunity row is locked, so stale outer rows never create notifications.
func (a *App) Scan(ctx context.Context) error {
	now := a.now().UTC()
	acquired, err := a.acquireLease(ctx, now)
	if err != nil || !acquired {
		return err
	}
	if err = a.renewLease(ctx); err != nil {
		return err
	}
	if err = a.cancelInactiveAlerts(ctx, now); err != nil {
		return err
	}
	var afterID uint64
	for {
		if err = a.renewLease(ctx); err != nil {
			return err
		}
		var candidates []scanOpportunity
		err = a.db.WithContext(ctx).Table("crm_opportunities").
			Select("id,tenant_id,opportunity_no,name,owner_user_id,current_stage,opp_status AS status,stage_changed_at").
			Where("id>? AND deleted_at IS NULL AND opp_status=? AND current_stage IN ?", afterID, statusFollowing, timedStages()).
			Order("id").Limit(a.batchSize).Scan(&candidates).Error
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			if err = a.renewLease(ctx); err != nil {
				return err
			}
			if err = a.scanOne(ctx, candidate, now); err != nil {
				return err
			}
		}
		if err = a.renewLease(ctx); err != nil {
			return err
		}
		var done bool
		afterID, done = nextPage(afterID, candidates, a.batchSize)
		if done {
			break
		}
	}
	if err = a.renewLease(ctx); err != nil {
		return err
	}
	return a.deliverSiteMessages(ctx, now)
}

func (a *App) acquireLease(ctx context.Context, now time.Time) (bool, error) {
	acquired := false
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`INSERT IGNORE INTO crm_opportunity_alert_job_leases(job_name,owner_id,lease_until,updated_at) VALUES(?,?,?,?)`, leaseName, a.workerID, now.Add(a.leaseDuration), now)
		if result.Error != nil {
			return result.Error
		}
		var lease struct {
			OwnerID    string
			LeaseUntil time.Time
		}
		if err := tx.Table("crm_opportunity_alert_job_leases").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("owner_id,lease_until").Where("job_name=?", leaseName).Take(&lease).Error; err != nil {
			return err
		}
		if lease.OwnerID == a.workerID || !lease.LeaseUntil.After(now) {
			if err := tx.Table("crm_opportunity_alert_job_leases").Where("job_name=?", leaseName).
				Updates(map[string]any{"owner_id": a.workerID, "lease_until": now.Add(a.leaseDuration), "updated_at": now}).Error; err != nil {
				return err
			}
			acquired = true
		}
		return nil
	})
	return acquired, err
}

func leaseRenewSQL() string {
	return `UPDATE crm_opportunity_alert_job_leases
		SET lease_until=GREATEST(DATE_ADD(lease_until,INTERVAL 1000 MICROSECOND),?),updated_at=?
		WHERE job_name=? AND owner_id=? AND lease_until>=?`
}

func leaseRenewalAffectedRows(rowsAffected int64) error {
	if rowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (a *App) renewLease(ctx context.Context) error {
	now := a.now().UTC()
	result := a.db.WithContext(ctx).Exec(leaseRenewSQL(),
		now.Add(a.leaseDuration), now, leaseName, a.workerID, now)
	if result.Error != nil {
		return result.Error
	}
	return leaseRenewalAffectedRows(result.RowsAffected)
}

func (a *App) scanOne(ctx context.Context, candidate scanOpportunity, now time.Time) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current scanOpportunity
		err := tx.Table("crm_opportunities").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id,tenant_id,opportunity_no,name,owner_user_id,current_stage,opp_status AS status,stage_changed_at").
			Where("tenant_id=? AND id=? AND deleted_at IS NULL", candidate.TenantID, candidate.ID).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cancelOpportunityAlerts(tx, candidate.TenantID, candidate.ID, now)
		}
		if err != nil {
			return err
		}
		if !activeOpportunity(current) {
			return cancelOpportunityAlerts(tx, current.TenantID, current.ID, now)
		}
		var rules []stageRule
		if err = tx.Table("crm_opportunity_stage_alert_rules").
			Select("stage,threshold_hours,config_version").
			Where("tenant_id=? AND stage=? AND enabled=1 AND deleted_at IS NULL", current.TenantID, current.CurrentStage).
			Find(&rules).Error; err != nil {
			return err
		}
		desired := make(map[alertIdentity]bool, len(rules))
		for _, rule := range rules {
			identity, due, eligible := desiredAlertIdentity(current, rule, now)
			if !eligible {
				// Owner is the only recipient that this service can resolve from
				// authoritative local data. Missing owners fail closed.
				continue
			}
			desired[identity] = true
			if err = createAlert(tx, current, rule, due, identity.RecipientID, now); err != nil {
				return err
			}
		}
		return cancelObsolete(tx, current, desired, now)
	})
}

func alertDue(stageChangedAt time.Time, thresholdHours uint32) time.Time {
	return stageChangedAt.UTC().Add(time.Duration(thresholdHours) * time.Hour)
}

func desiredAlertIdentity(current scanOpportunity, rule stageRule, now time.Time) (alertIdentity, time.Time, bool) {
	due := alertDue(current.StageChangedAt, rule.ThresholdHours)
	if now.Before(due) || current.OwnerUserID == "" {
		return alertIdentity{}, due, false
	}
	return alertIdentity{Stage: current.CurrentStage, ThresholdVersion: rule.ConfigVersion, RecipientID: current.OwnerUserID}, due, true
}

func activeOpportunity(value scanOpportunity) bool {
	return value.Status == statusFollowing && isTimedStage(value.CurrentStage)
}

func timedStages() []string {
	return []string{
		opportunity.StageInitial, opportunity.StageRequirement, opportunity.StageSolution,
		opportunity.StageQuotation, opportunity.StageBid,
	}
}

func isTimedStage(stage string) bool {
	for _, candidate := range timedStages() {
		if stage == candidate {
			return true
		}
	}
	return false
}

func createAlert(tx *gorm.DB, current scanOpportunity, rule stageRule, due time.Time, recipient string, now time.Time) error {
	alert := opportunity.StageAlert{
		Model: databaseModel(current.TenantID, now), OpportunityID: current.ID,
		Stage: current.CurrentStage, ThresholdVersion: rule.ConfigVersion,
		BasisAt: current.StageChangedAt.UTC(), DueAt: due.UTC(), Status: opportunity.StageAlertPending,
		RecipientID: recipient,
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&alert)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"alert_id": alert.ID, "opportunity_id": current.ID, "opportunity_no": current.OpportunityNo,
		"stage": current.CurrentStage, "threshold_version": rule.ConfigVersion,
		"recipient_id": recipient, "basis_at": alert.BasisAt, "due_at": alert.DueAt,
		"path": "/customer-opportunity/opportunities/" + strconv.FormatUint(current.ID, 10),
	})
	if err != nil {
		return err
	}
	event := opportunity.OutboxEvent{
		EventID:  stableEventID(current.TenantID, current.ID, current.CurrentStage, rule.ConfigVersion, recipient),
		TenantID: current.TenantID, EventType: stageAlertEventType,
		AggregateType: stageAlertAggregateType, AggregateID: strconv.FormatUint(alert.ID, 10),
		Payload: payload, Status: statusOutboxPending, CreatedAt: now,
	}
	return tx.Create(&event).Error
}

func databaseModel(tenant string, now time.Time) database.Model {
	return database.Model{TenantID: tenant, CreatedBy: workerActor, UpdatedBy: workerActor, CreatedAt: now, UpdatedAt: now, Version: 1}
}

func stableEventID(tenant string, opportunityID uint64, stage string, version uint64, recipient string) string {
	identity := tenant + "/" + strconv.FormatUint(opportunityID, 10) + "/" + stage + "/" + strconv.FormatUint(version, 10) + "/" + recipient
	sum := sha256.Sum256([]byte(identity))
	return "opp-stage-alert-" + hex.EncodeToString(sum[:20])
}

func cancelObsolete(tx *gorm.DB, current scanOpportunity, desired map[alertIdentity]bool, now time.Time) error {
	var alerts []opportunity.StageAlert
	if err := tx.Where("tenant_id=? AND opportunity_id=? AND status IN ('PENDING','UNREAD')", current.TenantID, current.ID).Find(&alerts).Error; err != nil {
		return err
	}
	for _, alert := range alerts {
		identity := alertIdentity{Stage: alert.Stage, ThresholdVersion: alert.ThresholdVersion, RecipientID: alert.RecipientID}
		if desired[identity] {
			continue
		}
		if err := cancelActiveByID(tx, alert.ID, now); err != nil {
			return err
		}
	}
	return nil
}

func cancelOpportunityAlerts(tx *gorm.DB, tenant string, opportunityID uint64, now time.Time) error {
	var ids []uint64
	if err := tx.Model(&opportunity.StageAlert{}).
		Where("tenant_id=? AND opportunity_id=? AND status IN ('PENDING','UNREAD')", tenant, opportunityID).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := cancelActiveByID(tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

func cancelActiveByID(tx *gorm.DB, alertID uint64, now time.Time) error {
	if err := tx.Model(&opportunity.StageAlert{}).Where("id=? AND status IN ('PENDING','UNREAD')", alertID).
		Updates(map[string]any{"status": opportunity.StageAlertCancelled, "updated_at": now, "updated_by": workerActor, "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	return tx.Model(&opportunity.OutboxEvent{}).
		Where("event_type=? AND aggregate_type=? AND aggregate_id=? AND status=?", stageAlertEventType, stageAlertAggregateType, strconv.FormatUint(alertID, 10), statusOutboxPending).
		Update("status", statusOutboxCancelled).Error
}

func (a *App) cancelInactiveAlerts(ctx context.Context, now time.Time) error {
	var afterID uint64
	for {
		if err := a.renewLease(ctx); err != nil {
			return err
		}
		var values []scanOpportunity
		err := a.db.WithContext(ctx).Table("crm_opportunities o").Distinct("o.id,o.tenant_id").
			Joins("JOIN crm_opportunity_stage_alerts a ON a.tenant_id=o.tenant_id AND a.opportunity_id=o.id AND a.status IN ('PENDING','UNREAD')").
			Where("o.id>? AND (o.deleted_at IS NOT NULL OR o.opp_status<>? OR o.current_stage NOT IN ?)", afterID, statusFollowing, timedStages()).
			Order("o.id").Limit(a.batchSize).Scan(&values).Error
		if err != nil {
			return err
		}
		for _, value := range values {
			if err = a.renewLease(ctx); err != nil {
				return err
			}
			if err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return cancelOpportunityAlerts(tx, value.TenantID, value.ID, now)
			}); err != nil {
				return err
			}
		}
		var done bool
		afterID, done = nextPage(afterID, values, a.batchSize)
		if done {
			return a.renewLease(ctx)
		}
	}
}

func (a *App) deliverSiteMessages(ctx context.Context, now time.Time) error {
	for {
		if err := a.renewLease(ctx); err != nil {
			return err
		}
		processed := 0
		err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var events []opportunity.OutboxEvent
			if err := tx.Raw(`SELECT * FROM crm_outbox_events WHERE event_type=? AND status=? ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`, stageAlertEventType, statusOutboxPending, a.batchSize).Scan(&events).Error; err != nil {
				return err
			}
			processed = len(events)
			for _, event := range events {
				alertID, parseErr := strconv.ParseUint(event.AggregateID, 10, 64)
				if parseErr != nil {
					return fmt.Errorf("invalid opportunity stage alert aggregate id")
				}
				var alert opportunity.StageAlert
				err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=?", alertID, opportunity.StageAlertPending).Take(&alert).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if updateErr := tx.Model(&opportunity.OutboxEvent{}).Where("id=? AND status=?", event.ID, statusOutboxPending).Update("status", statusOutboxCancelled).Error; updateErr != nil {
						return updateErr
					}
					continue
				}
				if err != nil {
					return err
				}
				if err = tx.Model(&opportunity.StageAlert{}).Where("id=? AND status=?", alert.ID, opportunity.StageAlertPending).
					Updates(map[string]any{"status": opportunity.StageAlertUnread, "sent_at": now, "updated_at": now, "updated_by": workerActor, "version": gorm.Expr("version+1")}).Error; err != nil {
					return err
				}
				if err = tx.Model(&opportunity.OutboxEvent{}).Where("id=? AND status=?", event.ID, statusOutboxPending).
					Updates(map[string]any{"status": statusOutboxSent, "sent_at": now}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err = a.renewLease(ctx); err != nil {
			return err
		}
		if processed < a.batchSize {
			return nil
		}
	}
}

func nextPage(previous uint64, values []scanOpportunity, batchSize int) (uint64, bool) {
	if len(values) == 0 {
		return previous, true
	}
	next := values[len(values)-1].ID
	return next, len(values) < batchSize
}
