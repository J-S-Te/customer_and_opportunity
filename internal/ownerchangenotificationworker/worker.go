package ownerchangenotificationworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ownerChangeEventType = "OPPORTUNITY_OWNER_CHANGED_NOTIFICATION"
	ownerChangeOperation = "OWNER_CHANGE"
	workerActor          = "opportunity-owner-notification-worker"
	statusPending        = "PENDING"
	statusRetryWait      = "RETRY_WAIT"
	statusProcessing     = "PROCESSING"
	statusSent           = "SENT"
	statusCancelled      = "CANCELLED"
	statusDeadLetter     = "DEAD_LETTER"
)

var errLeaseLost = errors.New("opportunity owner notification event lease lost")

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
	return &App{db: db, workerID: config.WorkerID, pollInterval: config.PollInterval, leaseDuration: config.LeaseDuration, batchSize: config.BatchSize, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (a *App) Close() error {
	db, err := a.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (a *App) Run(ctx context.Context) error {
	if _, err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

func (a *App) RunOnce(ctx context.Context) (int, error) {
	total := 0
	for {
		events, err := a.claim(ctx, a.now().UTC())
		if err != nil {
			return total, err
		}
		for _, event := range events {
			if err = a.process(ctx, event); err != nil {
				if errors.Is(err, errLeaseLost) {
					return total, err
				}
				if retryErr := a.retry(ctx, event, a.now().UTC(), "transient database processing failure"); retryErr != nil {
					return total, retryErr
				}
			}
			total++
		}
		if len(events) < a.batchSize {
			return total, nil
		}
	}
}

func (a *App) claim(ctx context.Context, now time.Time) ([]opportunity.OutboxEvent, error) {
	var result []opportunity.OutboxEvent
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []opportunity.OutboxEvent
		if err := tx.Raw(claimSQL(), ownerChangeEventType, statusPending, statusRetryWait, now, statusProcessing, now, a.batchSize).Scan(&events).Error; err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		lockedUntil := now.Add(a.leaseDuration)
		if err := tx.Model(&opportunity.OutboxEvent{}).Where("id IN ?", ids).
			Updates(map[string]any{"status": statusProcessing, "locked_by": a.workerID, "locked_until": lockedUntil}).Error; err != nil {
			return err
		}
		for index := range events {
			events[index].Status, events[index].LockedBy, events[index].LockedUntil = statusProcessing, a.workerID, &lockedUntil
		}
		result = events
		return nil
	})
	return result, err
}

func claimSQL() string {
	return `SELECT * FROM crm_outbox_events
WHERE event_type=? AND ((status IN (?,?) AND (next_retry_at IS NULL OR next_retry_at<=?))
 OR (status=? AND locked_until<?))
ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`
}

type ownerPayload struct {
	OpportunityID   uint64 `json:"opportunity_id"`
	OpportunityNo   string `json:"opportunity_no"`
	OpportunityName string `json:"opportunity_name"`
	RecipientUserID string `json:"recipient_user_id"`
	RecipientKind   string `json:"recipient_kind"`
	OwnerUserID     string `json:"owner_user_id"`
	TargetPath      string `json:"target_path"`
	Version         uint64 `json:"version"`
}

type opportunityState struct {
	ID            uint64
	TenantID      string
	OpportunityNo string
	Name          string
	OwnerUserID   string
	Status        string
	Version       uint64
	DeletedAt     *time.Time
}

type ownerAudit struct {
	ID         uint64
	BeforeJSON []byte
	AfterJSON  []byte
}

type ownerSnapshot struct {
	OwnerUserID string `json:"owner_user_id"`
	Version     uint64 `json:"version"`
}

func (a *App) process(ctx context.Context, event opportunity.OutboxEvent) error {
	now := a.now().UTC()
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked opportunity.OutboxEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, ownerChangeEventType, statusProcessing, a.workerID, now).Take(&locked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errLeaseLost
			}
			return err
		}
		payload, reason, valid := validatePayload(locked)
		if !valid {
			return a.deadLetter(tx, locked, now, reason)
		}
		var current opportunityState
		if err := tx.Table("crm_opportunities").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id,tenant_id,opportunity_no,name,owner_user_id,opp_status AS status,version,deleted_at").
			Where("tenant_id=? AND id=?", locked.TenantID, payload.OpportunityID).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.cancel(tx, locked, now, "opportunity not found")
			}
			return err
		}
		valid, reason, validateErr := validateAgainstDatabase(tx, locked, payload, current)
		if validateErr != nil {
			return validateErr
		}
		if !valid {
			if strings.Contains(reason, "audit") || strings.Contains(reason, "recipient") || strings.Contains(reason, "invalid") {
				return a.deadLetter(tx, locked, now, reason)
			}
			return a.cancel(tx, locked, now, reason)
		}
		if err := cancelObsoleteUnread(tx, locked.TenantID, current.ID, payload.Version, locked.EventID, now); err != nil {
			return err
		}
		message := project(locked, payload, current, now)
		if createErr := tx.Clauses(notificationConflictClause()).Create(&message).Error; createErr != nil {
			return createErr
		}
		return a.finish(tx, locked, now, statusSent, "")
	})
}

func cancelObsoleteUnread(tx *gorm.DB, tenantID string, opportunityID, currentVersion uint64, currentSourceEventID string, now time.Time) error {
	return tx.Model(&notification.Notification{}).
		Where(obsoleteUnreadWhere(), tenantID, opportunityID, notification.TypeOpportunityOwnerChanged, notification.StatusUnread, currentVersion, currentSourceEventID).
		Updates(map[string]any{"status": notification.StatusCancelled, "updated_at": now, "updated_by": workerActor, "version": gorm.Expr("version+1")}).Error
}

func obsoleteUnreadWhere() string {
	return "tenant_id=? AND opportunity_id=? AND type=? AND status=? AND opportunity_version<? AND source_event_id<>? AND deleted_at IS NULL"
}

func notificationConflictClause() clause.OnConflict {
	return clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "source_event_id"}}, DoNothing: true}
}

func validatePayload(event opportunity.OutboxEvent) (ownerPayload, string, bool) {
	var payload ownerPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return payload, "invalid JSON payload", false
	}
	if event.AggregateType != "opportunity" || event.AggregateID != strconv.FormatUint(payload.OpportunityID, 10) ||
		payload.OpportunityID == 0 || payload.Version == 0 || strings.TrimSpace(payload.RecipientUserID) == "" ||
		strings.TrimSpace(payload.OwnerUserID) == "" || !validRecipientKind(payload.RecipientKind) {
		return payload, "invalid owner change payload", false
	}
	expectedID := stableOwnerEventID(event.TenantID, payload.OpportunityID, payload.Version, payload.RecipientKind)
	if event.EventID != expectedID {
		return payload, "event identity does not match payload", false
	}
	return payload, "", true
}

func validRecipientKind(kind string) bool {
	return kind == notification.RecipientPreviousOwner || kind == notification.RecipientNewOwner
}

func validateAgainstDatabase(tx *gorm.DB, event opportunity.OutboxEvent, payload ownerPayload, current opportunityState) (bool, string, error) {
	var audit ownerAudit
	err := tx.Table("crm_audit_events").Select("id,before_json,after_json").
		Where("tenant_id=? AND module='opportunity' AND operation=? AND resource_type='opportunity' AND resource_id=? AND result='SUCCESS'", event.TenantID, ownerChangeOperation, event.AggregateID).
		Where("CAST(JSON_UNQUOTE(JSON_EXTRACT(after_json,'$.version')) AS UNSIGNED)=?", payload.Version).
		Order("id DESC").Take(&audit).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, "matching owner change audit not found", nil
	}
	if err != nil {
		return false, "", err
	}
	var before, after ownerSnapshot
	if json.Unmarshal(audit.BeforeJSON, &before) != nil || json.Unmarshal(audit.AfterJSON, &after) != nil {
		return false, "invalid owner change audit snapshot", nil
	}
	// Once a later owner transfer commits, both messages from an older transfer
	// are stale. This prevents queue delay from surfacing an obsolete handover.
	var later int64
	err = tx.Table("crm_audit_events").Where("tenant_id=? AND module='opportunity' AND operation=? AND resource_type='opportunity' AND resource_id=? AND result='SUCCESS' AND id>?", event.TenantID, ownerChangeOperation, event.AggregateID, audit.ID).Count(&later).Error
	if err != nil {
		return false, "", err
	}
	valid, reason := validateAuditProjection(payload, before, after, current, later)
	return valid, reason, nil
}

func validateAuditProjection(payload ownerPayload, before, after ownerSnapshot, current opportunityState, later int64) (bool, string) {
	if after.OwnerUserID != payload.OwnerUserID || after.Version != payload.Version {
		return false, "owner change audit does not match payload"
	}
	switch payload.RecipientKind {
	case notification.RecipientPreviousOwner:
		if before.OwnerUserID != payload.RecipientUserID || before.OwnerUserID == after.OwnerUserID {
			return false, "previous owner recipient does not match audit"
		}
	case notification.RecipientNewOwner:
		if after.OwnerUserID != payload.RecipientUserID {
			return false, "new owner recipient does not match audit"
		}
	default:
		return false, "invalid recipient kind"
	}
	if later > 0 || current.Version < payload.Version || current.OwnerUserID != after.OwnerUserID || current.Status == opportunity.StatusVoid || current.DeletedAt != nil {
		return false, "owner change was superseded or opportunity is inactive"
	}
	return true, ""
}

func project(event opportunity.OutboxEvent, payload ownerPayload, current opportunityState, now time.Time) notification.Notification {
	title, body := "您已接手商机", "您已成为该商机的新负责人。"
	if payload.RecipientKind == notification.RecipientPreviousOwner {
		title, body = "商机已完成负责人交接", "您已不再是该商机负责人。"
	}
	path := "/customer-opportunity/opportunities?opportunity_id=" + strconv.FormatUint(current.ID, 10)
	return notification.Notification{
		Model:         database.Model{TenantID: event.TenantID, CreatedBy: workerActor, UpdatedBy: workerActor, CreatedAt: now, UpdatedAt: now, Version: 1},
		SourceEventID: event.EventID, Type: notification.TypeOpportunityOwnerChanged,
		OpportunityID: current.ID, OpportunityVersion: payload.Version, OpportunityNo: current.OpportunityNo, OpportunityName: current.Name,
		RecipientID: payload.RecipientUserID, RecipientKind: payload.RecipientKind,
		Title: title, Body: body, TargetPath: path, Status: notification.StatusUnread,
	}
}

func (a *App) deadLetter(tx *gorm.DB, event opportunity.OutboxEvent, now time.Time, summary string) error {
	return a.finishWithRetry(tx, event, now, statusDeadLetter, nil, event.RetryCount+1, summary)
}

func (a *App) cancel(tx *gorm.DB, event opportunity.OutboxEvent, now time.Time, summary string) error {
	return a.finish(tx, event, now, statusCancelled, summary)
}

func (a *App) finish(tx *gorm.DB, event opportunity.OutboxEvent, now time.Time, status, summary string) error {
	return a.finishWithRetry(tx, event, now, status, nil, event.RetryCount, summary)
}

func (a *App) finishWithRetry(tx *gorm.DB, event opportunity.OutboxEvent, now time.Time, status string, next *time.Time, retry uint8, summary string) error {
	updates := map[string]any{"status": status, "retry_count": retry, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)}
	if status == statusSent {
		updates["sent_at"] = now
	}
	result := tx.Model(&opportunity.OutboxEvent{}).Where("id=? AND status=? AND locked_by=?", event.ID, statusProcessing, a.workerID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (a *App) retry(ctx context.Context, event opportunity.OutboxEvent, now time.Time, summary string) error {
	attempt := event.RetryCount + 1
	status, next := failurePlan(now, attempt)
	result := a.db.WithContext(ctx).Model(&opportunity.OutboxEvent{}).
		Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, ownerChangeEventType, statusProcessing, a.workerID, now).
		Updates(map[string]any{"status": status, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

var retryDelays = [...]time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}

func failurePlan(now time.Time, attempt uint8) (string, *time.Time) {
	if attempt > 0 && int(attempt) <= len(retryDelays) {
		next := now.Add(retryDelays[attempt-1])
		return statusRetryWait, &next
	}
	return statusDeadLetter, nil
}

func sanitize(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func stableOwnerEventID(tenant string, opportunityID, version uint64, kind string) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + strconv.FormatUint(opportunityID, 10) + "\x00" + strconv.FormatUint(version, 10) + "\x00" + kind))
	return hex.EncodeToString(sum[:])
}
