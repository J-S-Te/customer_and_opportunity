package presaleprogressnotificationworker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	workerActor      = "presale-progress-notification-worker"
	statusPending    = "PENDING"
	statusRetryWait  = "RETRY_WAIT"
	statusProcessing = "PROCESSING"
	statusSent       = "SENT"
	statusDeadLetter = "DEAD_LETTER"
)

var errLeaseLost = errors.New("presale progress notification lease lost")

type App struct {
	db            *gorm.DB
	config        Config
	now           func() time.Time
	newClaimToken func(string) (string, error)
}

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	return &App{db: db, config: config, now: time.Now, newClaimToken: claimToken}, nil
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
	ticker := time.NewTicker(a.config.PollInterval)
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
	processed := 0
	for processed < a.config.BatchSize {
		token, err := a.newClaimToken(a.config.WorkerID)
		if err != nil {
			return processed, err
		}
		now := a.now().UTC()
		event, found, err := a.claimOne(ctx, now, token)
		if err != nil {
			return processed, err
		}
		if !found {
			return processed, nil
		}
		if err = a.process(ctx, event, token); err != nil {
			if errors.Is(err, errLeaseLost) {
				return processed, err
			}
			if retryErr := a.retry(ctx, event, token, a.now().UTC(), "transient database processing failure"); retryErr != nil {
				return processed, retryErr
			}
		}
		processed++
	}
	return processed, nil
}

func claimSQL() string {
	return `SELECT * FROM crm_outbox_events
WHERE event_type=? AND ((status IN (?,?) AND (next_retry_at IS NULL OR next_retry_at<=?))
 OR (status=? AND locked_until<?))
ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`
}

func (a *App) claimOne(ctx context.Context, now time.Time, token string) (presale.OutboxEvent, bool, error) {
	var event presale.OutboxEvent
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(claimSQL(), presale.ProgressNotificationOutboxEventType, statusPending, statusRetryWait, now, statusProcessing, now).Scan(&event).Error; err != nil {
			return err
		}
		if event.ID == 0 {
			return nil
		}
		until := now.Add(a.config.LeaseDuration)
		result := tx.Model(&presale.OutboxEvent{}).Where("id=? AND event_type=?", event.ID, presale.ProgressNotificationOutboxEventType).
			Updates(map[string]any{"status": statusProcessing, "locked_by": token, "locked_until": until})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		event.Status, event.LockedBy, event.LockedUntil = statusProcessing, token, &until
		return nil
	})
	return event, event.ID != 0 && err == nil, err
}

type eventPayload struct {
	ProgressNotificationEventID uint64 `json:"progress_notification_event_id"`
}

func (a *App) process(ctx context.Context, candidate presale.OutboxEvent, token string) error {
	now := a.now().UTC()
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var outbox presale.OutboxEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", candidate.ID, presale.ProgressNotificationOutboxEventType, statusProcessing, token, now).
			Take(&outbox).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errLeaseLost
			}
			return err
		}
		payload, reason, valid := validatePayload(outbox)
		if !valid {
			return a.finish(tx, outbox, token, now, statusDeadLetter, outbox.RetryCount+1, reason, nil)
		}
		var evidence presale.ProgressNotificationEvent
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND id=?", outbox.TenantID, payload.ProgressNotificationEventID).Take(&evidence).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.finish(tx, outbox, token, now, statusDeadLetter, outbox.RetryCount+1, "progress notification evidence not found", nil)
			}
			return err
		}
		var progress presale.ProgressLog
		if err := tx.Where("tenant_id=? AND id=?", outbox.TenantID, evidence.ProgressID).Take(&progress).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.finish(tx, outbox, token, now, statusDeadLetter, outbox.RetryCount+1, "progress record not found", nil)
			}
			return err
		}
		var request presale.PresaleRequest
		if err := tx.Where("tenant_id=? AND id=?", outbox.TenantID, evidence.RequestID).Take(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.finish(tx, outbox, token, now, statusDeadLetter, outbox.RetryCount+1, "presale request not found", nil)
			}
			return err
		}
		var authorAssignmentCount int64
		if err := tx.Unscoped().Model(&presale.Assignment{}).
			Where("tenant_id=? AND request_id=? AND assignee_id=? AND assigned_at<=? AND (ended_at IS NULL OR ended_at>?)", outbox.TenantID, evidence.RequestID, evidence.AuthorPersonID, evidence.OccurredAt, evidence.OccurredAt).
			Count(&authorAssignmentCount).Error; err != nil {
			return err
		}
		if authorAssignmentCount == 0 {
			return a.finish(tx, outbox, token, now, statusDeadLetter, outbox.RetryCount+1, "progress author assignment not found at occurrence time", nil)
		}
		var assignment *presale.Assignment
		if evidence.AssignmentID > 0 {
			var value presale.Assignment
			if err := tx.Unscoped().Where("tenant_id=? AND id=?", outbox.TenantID, evidence.AssignmentID).Take(&value).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return a.finish(tx, outbox, token, now, statusDeadLetter, outbox.RetryCount+1, "assignment not found", nil)
				}
				return err
			}
			assignment = &value
		}
		if reason = validateEvidence(outbox, evidence, progress, request, assignment); reason != "" {
			return a.finish(tx, outbox, token, now, statusDeadLetter, outbox.RetryCount+1, reason, nil)
		}
		message := project(outbox, evidence, request, now)
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "source_event_id"}}, DoNothing: true}).Create(&message).Error; err != nil {
			return err
		}
		return a.finish(tx, outbox, token, now, statusSent, outbox.RetryCount, "", nil)
	})
}

func validatePayload(event presale.OutboxEvent) (eventPayload, string, bool) {
	var payload eventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.ProgressNotificationEventID == 0 {
		return payload, "invalid progress notification payload", false
	}
	if event.AggregateType != "presale_progress_notification_event" || event.AggregateID != strconv.FormatUint(payload.ProgressNotificationEventID, 10) {
		return payload, "invalid progress notification aggregate", false
	}
	return payload, "", true
}

func validateEvidence(outbox presale.OutboxEvent, evidence presale.ProgressNotificationEvent, progress presale.ProgressLog, request presale.PresaleRequest, assignment *presale.Assignment) string {
	if evidence.EventID != outbox.EventID || evidence.TenantID != outbox.TenantID || evidence.RequestID != request.ID ||
		evidence.ProgressID != progress.ID || progress.RequestID != request.ID || progress.AuthorID != evidence.AuthorUserID ||
		!progress.CreatedAt.Equal(evidence.OccurredAt) || evidence.AuthorUserID == "" || evidence.AuthorPersonID == "" || evidence.RecipientID == "" ||
		presale.ProgressNotificationEventID(evidence.TenantID, evidence.RequestID, evidence.ProgressID, evidence.AssignmentID, evidence.RecipientNamespace, evidence.RecipientID, evidence.RecipientKind) != evidence.EventID {
		return "progress notification evidence mismatch"
	}
	switch evidence.RecipientNamespace {
	case presale.ProgressRecipientUser:
		if evidence.RecipientKind != presale.ProgressRecipientApplicant || evidence.AssignmentID != 0 || assignment != nil ||
			request.ApplicantID != evidence.RecipientID || evidence.RecipientID == evidence.AuthorUserID {
			return "progress applicant evidence mismatch"
		}
	case presale.ProgressRecipientPerson:
		if evidence.RecipientKind != presale.ProgressRecipientAssignee || evidence.AssignmentID == 0 || assignment == nil ||
			assignment.RequestID != evidence.RequestID || assignment.AssigneeID != evidence.RecipientID || evidence.RecipientID == evidence.AuthorPersonID ||
			assignment.AssignedAt.After(evidence.OccurredAt) || (assignment.EndedAt != nil && !assignment.EndedAt.After(evidence.OccurredAt)) {
			return "progress assignee evidence mismatch"
		}
	default:
		return "invalid progress recipient namespace"
	}
	return ""
}

func project(outbox presale.OutboxEvent, evidence presale.ProgressNotificationEvent, request presale.PresaleRequest, now time.Time) notification.Notification {
	typeCode, kind := notification.TypePresaleProgressApplicant, notification.RecipientProgressApplicant
	if evidence.RecipientNamespace == presale.ProgressRecipientPerson {
		typeCode, kind = notification.TypePresaleProgressAssignee, notification.RecipientProgressAssignee
	}
	return notification.Notification{
		Model:         database.Model{TenantID: outbox.TenantID, CreatedBy: workerActor, UpdatedBy: workerActor, CreatedAt: now, UpdatedAt: now, Version: 1},
		SourceEventID: outbox.EventID, Type: typeCode, OpportunityID: request.OpportunityID,
		OpportunityNo: request.OpportunityNoSnapshot, RequestID: request.ID, RequestNo: request.RequestNo,
		AssignmentID: evidence.AssignmentID, ProgressID: evidence.ProgressID, RecipientID: evidence.RecipientID,
		RecipientKind: kind, Title: "售前任务有新的过程记录", Body: "任务执行人已追加一条过程记录，请进入任务详情查看。",
		TargetPath: "/customer-opportunity/presale?request_id=" + strconv.FormatUint(request.ID, 10), Status: notification.StatusUnread,
	}
}

func (a *App) retry(ctx context.Context, event presale.OutboxEvent, token string, now time.Time, summary string) error {
	attempt := event.RetryCount + 1
	status, next := failurePlan(now, attempt)
	result := a.db.WithContext(ctx).Model(&presale.OutboxEvent{}).
		Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, presale.ProgressNotificationOutboxEventType, statusProcessing, token, now).
		Updates(map[string]any{"status": status, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (a *App) finish(tx *gorm.DB, event presale.OutboxEvent, token string, now time.Time, status string, retry uint8, summary string, next *time.Time) error {
	updates := map[string]any{"status": status, "retry_count": retry, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)}
	if status == statusSent {
		updates["sent_at"] = now
	}
	result := tx.Model(&presale.OutboxEvent{}).
		Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, presale.ProgressNotificationOutboxEventType, statusProcessing, token, now).
		Updates(updates)
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

func claimToken(workerID string) (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return workerID + "." + base64.RawURLEncoding.EncodeToString(value), nil
}

func sanitize(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
