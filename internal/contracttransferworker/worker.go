package contracttransferworker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	eventType        = "OPPORTUNITY_SIGNED"
	statusPending    = "PENDING"
	statusRetryWait  = "RETRY_WAIT"
	statusProcessing = "PROCESSING"
	statusSent       = "SENT"
	statusDeadLetter = "DEAD_LETTER"
)

var errLeaseLost = errors.New("contract transfer lease lost")

type attemptRecord struct {
	ID               uint64 `gorm:"primaryKey;autoIncrement"`
	TenantID         string
	SourceEventID    string
	AttemptNo        uint8
	Result           string
	ContractIntakeID string
	ResponseCode     string
	ErrorSummary     string
	AttemptedAt      time.Time
}

func (attemptRecord) TableName() string { return "crm_contract_transfer_attempts" }

type opportunityState struct {
	ID, CustomerID, Version                                                    uint64
	TenantID, OpportunityNo, CurrentStage, Status, ExpectedAmount, OwnerUserID string
	ContractRef, TerminalPendingType                                           string
	DeletedAt                                                                  *time.Time
}

type signedPayload struct {
	OpportunityID  uint64    `json:"opportunity_id"`
	OpportunityNo  string    `json:"opportunity_no"`
	CustomerID     uint64    `json:"customer_id"`
	ContractRef    string    `json:"contract_ref"`
	ExpectedAmount string    `json:"expected_amount"`
	EventVersion   uint64    `json:"event_version"`
	OccurredAt     time.Time `json:"occurred_at"`
}

type App struct {
	db         *gorm.DB
	store      transferStore
	client     deliveryClient
	config     Config
	now        func() time.Time
	claimToken func(string) (string, error)
}

type deliveryClient interface {
	deliver(context.Context, signedCommand) (deliveryResult, error)
}

type transferStore interface {
	// 领取、权威状态复核与最终落账被拆成独立阶段：网络调用期间不持有数据库事务，
	// 最终写回再用每次领取生成的 fencing token 拒绝租约过期后的迟到结果。
	claimOne(context.Context, time.Time, time.Duration, string) (opportunity.OutboxEvent, bool, error)
	authoritativeCommand(context.Context, opportunity.OutboxEvent, time.Time, string) (signedCommand, string, error)
	finish(context.Context, opportunity.OutboxEvent, time.Time, string, string, string, string) error
	retry(context.Context, opportunity.OutboxEvent, time.Time, string, string) error
}

type gormTransferStore struct {
	db *gorm.DB
}

func New(config Config) (*App, error) {
	client, err := newContractClient(config)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(mysqlDialector(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	return &App{
		db:         db,
		store:      &gormTransferStore{db: db},
		client:     client,
		config:     config,
		now:        time.Now,
		claimToken: integrationClaimToken,
	}, nil
}

// 将 Dialector 留作可替换依赖，使测试能够验证领取和落账事务，而无需启动真实 MySQL。
var mysqlDialector = func(dsn string) gorm.Dialector { return mysql.Open(dsn) }

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
		// token 按“本次领取”生成而不是按进程固定，避免同一 WorkerID 重启后接受旧进程的迟到写回。
		token, err := a.claimToken(a.config.WorkerID)
		if err != nil {
			return processed, err
		}
		now := a.now().UTC()
		event, found, err := a.store.claimOne(ctx, now, a.config.LeaseDuration, token)
		if err != nil {
			return processed, err
		}
		if !found {
			return processed, nil
		}
		if err = a.process(ctx, event, token); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func claimSQL() string {
	return `SELECT * FROM crm_outbox_events
WHERE event_type=?
  AND ((status IN (?,?) AND (next_retry_at IS NULL OR next_retry_at<=?))
       OR (status=? AND locked_until<?))
ORDER BY created_at,id
LIMIT 1 FOR UPDATE SKIP LOCKED`
}

func (s *gormTransferStore) claimOne(ctx context.Context, now time.Time, lease time.Duration, token string) (opportunity.OutboxEvent, bool, error) {
	var result opportunity.OutboxEvent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SKIP LOCKED 让多个副本并行领取不同事件；这里只建立有限租约，绝不在行锁内调用合同系统。
		if err := tx.Raw(claimSQL(), eventType, statusPending, statusRetryWait, now, statusProcessing, now).Scan(&result).Error; err != nil {
			return err
		}
		if result.ID == 0 {
			return nil
		}
		until := now.Add(lease)
		updated := tx.Model(&opportunity.OutboxEvent{}).
			Where("id=? AND event_type=?", result.ID, eventType).
			Updates(map[string]any{"status": statusProcessing, "locked_by": token, "locked_until": until})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errLeaseLost
		}
		result.Status = statusProcessing
		result.LockedBy = token
		result.LockedUntil = &until
		return nil
	})
	return result, result.ID != 0 && err == nil, err
}

func (a *App) process(ctx context.Context, event opportunity.OutboxEvent, token string) error {
	now := a.now().UTC()
	command, permanentReason, err := a.store.authoritativeCommand(ctx, event, now, token)
	if err != nil {
		if errors.Is(err, errLeaseLost) {
			return errLeaseLost
		}
		return a.store.retry(ctx, event, now, token, "CRM authority read failed")
	}
	if permanentReason != "" {
		// 事件身份或商机终态已经不成立时，重试不会改变结果，直接进入死信便于人工审计。
		return a.store.finish(ctx, event, now, token, statusDeadLetter, "", permanentReason)
	}
	result, err := a.client.deliver(ctx, command)
	finishedAt := a.now().UTC()
	if err != nil {
		var permanent permanentDeliveryError
		if errors.As(err, &permanent) {
			return a.store.finish(ctx, event, finishedAt, token, statusDeadLetter, "", permanent.Error())
		}
		return a.store.retry(ctx, event, finishedAt, token, err.Error())
	}
	return a.store.finish(ctx, event, finishedAt, token, statusSent, result.IntakeID, "")
}

func (s *gormTransferStore) authoritativeCommand(ctx context.Context, event opportunity.OutboxEvent, now time.Time, token string) (signedCommand, string, error) {
	var command signedCommand
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked opportunity.OutboxEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, eventType, statusProcessing, token, now).Take(&locked).Error; err != nil {
			return errLeaseLost
		}
		var payload signedPayload
		if json.Unmarshal(locked.Payload, &payload) != nil || locked.AggregateType != "opportunity" || locked.AggregateID != strconv.FormatUint(payload.OpportunityID, 10) || locked.EventID != stableEventID(locked.TenantID, payload.OpportunityID, payload.EventVersion) {
			return permanentValidationError{"invalid outbox identity or payload"}
		}
		var state opportunityState
		if err := tx.Table("crm_opportunities").Select("id,tenant_id,customer_id,version,opportunity_no,current_stage,opp_status AS status,CAST(expected_amount AS CHAR) AS expected_amount,owner_user_id,COALESCE(contract_ref,'') AS contract_ref,terminal_pending_type,deleted_at").Where("tenant_id=? AND id=?", locked.TenantID, payload.OpportunityID).Take(&state).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return permanentValidationError{"authoritative opportunity is absent"}
			}
			return err
		}
		if state.DeletedAt != nil || state.Version != payload.EventVersion || state.CurrentStage != opportunity.StageSigned || state.Status != opportunity.StatusClosed || state.ContractRef == "" || state.TerminalPendingType != opportunity.PendingNone {
			return permanentValidationError{"authoritative opportunity no longer matches accepted signed state"}
		}
		// Outbox 载荷中的展示字段可能已经陈旧，只保留事件身份和发生时间；合同指令中的业务值全部取自锁定后的 CRM 权威行。
		command = signedCommand{EventID: locked.EventID, TenantID: state.TenantID, OpportunityID: state.ID, EventVersion: state.Version, OpportunityNo: state.OpportunityNo, CustomerID: state.CustomerID, ContractRef: state.ContractRef, ExpectedAmount: normalizeAmount(state.ExpectedAmount), OccurredAt: locked.CreatedAt.UTC(), SourceRequestID: ""}
		return nil
	})
	var permanent permanentValidationError
	if errors.As(err, &permanent) {
		return signedCommand{}, permanent.reason, nil
	}
	return command, "", err
}

type permanentValidationError struct{ reason string }

func (e permanentValidationError) Error() string { return e.reason }

func (s *gormTransferStore) finish(ctx context.Context, event opportunity.OutboxEvent, now time.Time, token, status, intakeID, summary string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 尝试记录与 Outbox 终态在同一事务提交，避免出现“已发送但无审计记录”或相反的半完成状态。
		attempt := event.RetryCount + 1
		if err := tx.Create(&attemptRecord{TenantID: event.TenantID, SourceEventID: event.EventID, AttemptNo: attempt, Result: status, ContractIntakeID: intakeID, ResponseCode: status, ErrorSummary: sanitize(summary), AttemptedAt: now}).Error; err != nil {
			return err
		}
		updates := map[string]any{"status": status, "retry_count": attempt, "next_retry_at": nil, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)}
		if status == statusSent {
			updates["sent_at"] = now
		}
		result := tx.Model(&opportunity.OutboxEvent{}).Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, eventType, statusProcessing, token, now).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		return nil
	})
}

func (s *gormTransferStore) retry(ctx context.Context, event opportunity.OutboxEvent, now time.Time, token, summary string) error {
	attempt := event.RetryCount + 1
	if int(attempt) > len(retryDelays) {
		return s.finish(ctx, event, now, token, statusDeadLetter, "", summary)
	}
	next := now.Add(retryDelays[attempt-1])
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 重试次数和下一次可见时间一起推进；超过有限退避表后转死信，防止永久故障形成热循环。
		if err := tx.Create(&attemptRecord{TenantID: event.TenantID, SourceEventID: event.EventID, AttemptNo: attempt, Result: statusRetryWait, ResponseCode: "RETRY", ErrorSummary: sanitize(summary), AttemptedAt: now}).Error; err != nil {
			return err
		}
		result := tx.Model(&opportunity.OutboxEvent{}).Where("id=? AND event_type=? AND status=? AND locked_by=? AND locked_until>=?", event.ID, eventType, statusProcessing, token, now).Updates(map[string]any{"status": statusRetryWait, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		return nil
	})
}

// 领取令牌同时保留 WorkerID 的不可逆诊断指纹和 256 位随机量；即使不同副本误配了相同主机名，也不会共享写回资格。
func integrationClaimToken(workerID string) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	workerHash := sha256.Sum256([]byte(workerID))
	return "ctw-" + hex.EncodeToString(workerHash[:8]) + "-" + base64.RawURLEncoding.EncodeToString(random), nil
}

var retryDelays = [...]time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}

func sanitize(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if len(v) > 500 {
		return v[:500]
	}
	return v
}
func normalizeAmount(v string) string {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 1 {
		return v + ".00"
	}
	if len(parts[1]) == 1 {
		return v + "0"
	}
	return v
}
func stableEventID(tenant string, id, version uint64) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + strconv.FormatUint(id, 10) + "\x00" + strconv.FormatUint(version, 10) + "\x00OPPORTUNITY_SIGNED"))
	return "opp-signed-" + hex.EncodeToString(sum[:16])
}
