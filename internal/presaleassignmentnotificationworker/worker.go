package presaleassignmentnotificationworker

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	eventType        = "PRESALE_ASSIGNMENT_SITE_NOTIFICATION"
	workerActor      = "presale-assignment-notification-worker"
	statusPending    = "PENDING"
	statusRetryWait  = "RETRY_WAIT"
	statusProcessing = "PROCESSING"
	statusSent       = "SENT"
	statusDeadLetter = "DEAD_LETTER"
)

var errLeaseLost = errors.New("presale assignment notification lease lost")

type App struct {
	db                          *gorm.DB
	workerID                    string
	pollInterval, leaseDuration time.Duration
	batchSize                   int
	now                         func() time.Time
}

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	return &App{db: db, workerID: config.WorkerID, pollInterval: config.PollInterval, leaseDuration: config.LeaseDuration, batchSize: config.BatchSize, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
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

func (a *App) claim(ctx context.Context, now time.Time) ([]presale.OutboxEvent, error) {
	var result []presale.OutboxEvent
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []presale.OutboxEvent
		if err := tx.Raw(claimSQL(), eventType, statusPending, statusRetryWait, now, statusProcessing, now, a.batchSize).Scan(&events).Error; err != nil {
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
		if err := tx.Model(&presale.OutboxEvent{}).Where("id IN ?", ids).Updates(map[string]any{"status": statusProcessing, "locked_by": a.workerID, "locked_until": lockedUntil}).Error; err != nil {
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

type eventPayload struct {
	AssignmentEventID uint64 `json:"assignment_event_id"`
}

func (a *App) process(ctx context.Context, candidate presale.OutboxEvent) error {
	now := a.now().UTC()
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var outbox presale.OutboxEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", candidate.ID, eventType, statusProcessing, a.workerID, now).Take(&outbox).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errLeaseLost
			}
			return err
		}
		payload, reason, valid := validatePayload(outbox)
		if !valid {
			return a.finish(tx, outbox, now, statusDeadLetter, outbox.RetryCount+1, reason, nil)
		}
		var evidence presale.AssignmentEvent
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND id=?", outbox.TenantID, payload.AssignmentEventID).Take(&evidence).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.finish(tx, outbox, now, statusDeadLetter, outbox.RetryCount+1, "assignment evidence not found", nil)
			}
			return err
		}
		var assignment presale.Assignment
		if err := tx.Unscoped().Where("tenant_id=? AND id=?", outbox.TenantID, evidence.AssignmentID).Take(&assignment).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.finish(tx, outbox, now, statusDeadLetter, outbox.RetryCount+1, "assignment not found", nil)
			}
			return err
		}
		var request presale.PresaleRequest
		if err := tx.Where("tenant_id=? AND id=?", outbox.TenantID, evidence.RequestID).Take(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.finish(tx, outbox, now, statusDeadLetter, outbox.RetryCount+1, "presale request not found", nil)
			}
			return err
		}
		if reason := validateEvidence(outbox, evidence, assignment, request); reason != "" {
			return a.finish(tx, outbox, now, statusDeadLetter, outbox.RetryCount+1, reason, nil)
		}
		message := project(outbox, evidence, request, now)
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "source_event_id"}}, DoNothing: true}).Create(&message).Error; err != nil {
			return err
		}
		return a.finish(tx, outbox, now, statusSent, outbox.RetryCount, "", nil)
	})
}

func validatePayload(event presale.OutboxEvent) (eventPayload, string, bool) {
	var payload eventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.AssignmentEventID == 0 {
		return payload, "invalid assignment notification payload", false
	}
	if event.AggregateType != "presale_assignment_event" || event.AggregateID != strconv.FormatUint(payload.AssignmentEventID, 10) {
		return payload, "invalid assignment notification aggregate", false
	}
	return payload, "", true
}

func validateEvidence(outbox presale.OutboxEvent, evidence presale.AssignmentEvent, assignment presale.Assignment, request presale.PresaleRequest) string {
	if evidence.EventID != outbox.EventID || evidence.TenantID != outbox.TenantID || evidence.RequestID != request.ID ||
		evidence.AssignmentID != assignment.ID || assignment.RequestID != evidence.RequestID || evidence.RecipientPersonID == "" || evidence.RecipientPersonID != assignment.AssigneeID ||
		evidence.PersonNameSnapshot != assignment.AssigneeNameSnapshot || evidence.RoleSnapshot != assignment.AssigneeRole ||
		presale.AssignmentNotificationEventID(evidence.TenantID, evidence.RequestID, evidence.AssignmentID, evidence.EventType) != evidence.EventID {
		return "assignment notification evidence mismatch"
	}
	switch evidence.EventType {
	case presale.AssignmentEventAdded:
		if !evidence.OccurredAt.Equal(assignment.AssignedAt) || evidence.ActorID != assignment.AssignedBy || evidence.ChangeReason != assignment.ChangeReason {
			return "assignment add evidence timestamp mismatch"
		}
	case presale.AssignmentEventRemoved:
		if assignment.EndedAt == nil || !evidence.OccurredAt.Equal(assignment.EndedAt.UTC()) || assignment.IsCurrent || evidence.ActorID != assignment.UpdatedBy {
			return "assignment removal evidence state mismatch"
		}
	default:
		return "invalid assignment event type"
	}
	return ""
}

func project(outbox presale.OutboxEvent, evidence presale.AssignmentEvent, request presale.PresaleRequest, now time.Time) notification.Notification {
	typeCode, kind, title, body := notification.TypePresaleAssigneeAdded, notification.RecipientAssigneeAdded, "您有新的售前任务", "您已被加入该售前任务的当前执行人。"
	if evidence.EventType == presale.AssignmentEventRemoved {
		typeCode, kind, title, body = notification.TypePresaleAssigneeRemoved, notification.RecipientAssigneeRemoved, "您已移出售前任务", "您已不再是该售前任务的当前执行人。"
	}
	return notification.Notification{
		Model:         database.Model{TenantID: outbox.TenantID, CreatedBy: workerActor, UpdatedBy: workerActor, CreatedAt: now, UpdatedAt: now, Version: 1},
		SourceEventID: outbox.EventID, Type: typeCode, OpportunityID: request.OpportunityID,
		OpportunityNo: request.OpportunityNoSnapshot, RequestID: request.ID, RequestNo: request.RequestNo, AssignmentID: evidence.AssignmentID,
		RecipientID: evidence.RecipientPersonID, RecipientKind: kind, Title: title, Body: body,
		TargetPath: "/customer-opportunity/presale?request_id=" + strconv.FormatUint(request.ID, 10), Status: notification.StatusUnread,
	}
}

func (a *App) retry(ctx context.Context, event presale.OutboxEvent, now time.Time, summary string) error {
	attempt := event.RetryCount + 1
	status, next := failurePlan(now, attempt)
	result := a.db.WithContext(ctx).Model(&presale.OutboxEvent{}).Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, eventType, statusProcessing, a.workerID, now).
		Updates(map[string]any{"status": status, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (a *App) finish(tx *gorm.DB, event presale.OutboxEvent, now time.Time, status string, retry uint8, summary string, next *time.Time) error {
	updates := map[string]any{"status": status, "retry_count": retry, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)}
	if status == statusSent {
		updates["sent_at"] = now
	}
	result := tx.Model(&presale.OutboxEvent{}).Where("id=? AND status=? AND locked_by=?", event.ID, statusProcessing, a.workerID).Updates(updates)
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
