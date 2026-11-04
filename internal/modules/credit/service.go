package credit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/businessclock"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db     *gorm.DB
	owners ownerdirectory.Catalog
	now    func() time.Time
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) UseOwnerDirectory(catalog ownerdirectory.Catalog) *Service {
	s.owners = catalog
	return s
}

func (s *Service) GetLevel(ctx context.Context, customerID uint64) (LevelResponse, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return LevelResponse{}, apperror.ErrUnauthenticated
	}
	var row struct {
		ID                  uint64
		CreditLevel, Source string
		OnTime, Late        uint32
		UpdatedAt           *time.Time
	}
	err := scopedCreditCustomers(database.FromContext(ctx, s.db), p).
		Select("crm_customers.id,crm_customers.credit_level,crm_customers.credit_change_source,crm_customers.credit_updated_at AS updated_at,crm_customers.consecutive_ontime_count AS on_time,crm_customers.consecutive_late_count AS late").
		Where("crm_customers.id=?", customerID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return LevelResponse{}, apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
	}
	if err != nil {
		return LevelResponse{}, err
	}
	result := LevelResponse{CustomerID: row.ID, Level: row.CreditLevel, UpdatedAt: row.UpdatedAt, Source: row.Source, ConsecutiveOntimeCount: row.OnTime, ConsecutiveLateCount: row.Late}
	var latest struct {
		PeriodNo string
		PaidDate *time.Time
	}
	if err := database.FromContext(ctx, s.db).Table("crm_customer_credit_payment_records").Select("period_no,paid_date").Where("tenant_id=? AND customer_id=? AND evaluation='LATE'", p.TenantID, customerID).Order("paid_date DESC,id DESC").Take(&latest).Error; err == nil {
		result.LastLatePeriodNo, result.LastLatePaidAt = latest.PeriodNo, latest.PaidDate
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return LevelResponse{}, err
	}
	if err := database.FromContext(ctx, s.db).Table("crm_customer_credit_payment_records").Where("tenant_id=? AND customer_id=? AND evaluation='LATE'", p.TenantID, customerID).Count(&result.RecentLateCount).Error; err != nil {
		return LevelResponse{}, err
	}
	return result, nil
}

func (s *Service) Statistics(ctx context.Context) (Statistics, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return Statistics{}, apperror.ErrUnauthenticated
	}
	var result Statistics
	err := scopedCreditCustomers(database.FromContext(ctx, s.db), p).
		Select("COALESCE(SUM(crm_customers.credit_level='C'),0) AS level_c,COALESCE(SUM(crm_customers.credit_level='D'),0) AS level_d,COALESCE(SUM(crm_customers.credit_level IN ('C','D')),0) AS attention_customers").
		Where("crm_customers.status='ACTIVE' AND crm_customers.merged_into_id IS NULL").
		Scan(&result).Error
	return result, err
}

// scopedCreditCustomers 把信用子模块收口到客户主数据的数据范围，直接复用
// customer.ApplyDataScope，不再在信用模块里重复维护一份范围谓词。
// 销售角色的可见客户是本人“当前负责”的客户（owner_user_id）：交接后旧负责人
// 立即失去访问，created_by 只是创建历史，不能作为访问依据。管理角色继续遵循
// 平台下发的 ALL/ORG 范围，未知或个人范围按负责人本人失败收窄。
func scopedCreditCustomers(db *gorm.DB, principal auth.Principal) *gorm.DB {
	return customer.ApplyDataScope(db.Table("crm_customers"), principal)
}

func creditSalesOnlyPrincipal(principal auth.Principal) bool {
	hasSales := false
	for _, role := range principal.Roles {
		if role == "sales" {
			hasSales = true
			break
		}
	}
	if !hasSales {
		return false
	}
	for _, role := range principal.Roles {
		switch role {
		case "sales_director", "customer_admin", "crm_super_admin", "auditor", "admin":
			return false
		}
	}
	return true
}

func ensureCreditCustomerVisible(db *gorm.DB, principal auth.Principal, customerID uint64) error {
	var row struct{ ID uint64 }
	err := scopedCreditCustomers(db, principal).
		Select("crm_customers.id").
		Where("crm_customers.id=?", customerID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
	}
	return err
}

// creditApplicationNotFound 用途：统一不存在与范围外申请的 404，避免存在性探测。
// 参数：无。
// 返回值：申请不存在业务错误。
func creditApplicationNotFound() error {
	return apperror.New(404, "CRM_CREDIT_APPLICATION_NOT_FOUND", "credit application not found")
}

// ensureCreditApplicationCustomerVisible 用途：按客户范围验证申请可见性，不可见折叠为申请 404。
// 参数：db 为查询会话；principal 为审批主体；customerID 为申请客户。
// 返回值：可见时 nil；不可见为 404；数据库故障保留原错误。
func ensureCreditApplicationCustomerVisible(db *gorm.DB, principal auth.Principal, customerID uint64) error {
	err := ensureCreditCustomerVisible(db, principal, customerID)
	if err == nil {
		return nil
	}
	var appErr *apperror.Error
	if errors.As(err, &appErr) && appErr.HTTPStatus == 404 {
		return creditApplicationNotFound()
	}
	return err
}

// lockScopedCreditCustomer 用途：在已锁申请的事务中按范围锁定可用客户，保持申请到客户锁序。
// 参数：tx 为事务；principal 为审批主体；customerID 为客户；dest 接收锁定行。
// 返回值：查询错误；无可见可用客户时返回记录不存在。
func lockScopedCreditCustomer(tx *gorm.DB, principal auth.Principal, customerID uint64, dest any) error {
	return scopedCreditCustomers(tx, principal).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("crm_customers.id,crm_customers.credit_level").
		Where("crm_customers.id=? AND crm_customers.status='ACTIVE' AND crm_customers.merged_into_id IS NULL", customerID).
		Take(dest).Error
}

func defaultRuleSettings(tenant string, now time.Time) RuleSettings {
	return RuleSettings{TenantID: tenant, GraceDays: 7, OnTimeThreshold: 2, LateThreshold: 2, LevelStep: 1, Enabled: true, UpdatedAt: now}
}

var creditReferencePattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

var allowedCreditSources = map[string]struct{}{
	"settlement": {}, "erp": {}, "finance": {}, "manual": {},
}

func validatePaymentEvent(event PaymentEvent, due, paid decimal.Decimal) error {
	if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.PaymentID) == "" || event.CustomerID == 0 || event.DueDate.IsZero() {
		return apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "event_id, payment_id, customer_id and due_date are required")
	}
	eventType := event.EventType
	if eventType == "" {
		eventType = PaymentEventCompleted
	}
	switch eventType {
	case PaymentEventCompleted:
		if event.ReversalOfPaymentID != "" {
			return apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "reversal_of_payment_id is only valid for a reversal event")
		}
	case PaymentEventReversed:
		if event.ReversalOfPaymentID == "" || !paid.IsZero() {
			return apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "reversal requires reversal_of_payment_id and zero paid_amount")
		}
	default:
		return apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "event_type is not supported")
	}
	source := strings.ToLower(strings.TrimSpace(event.SourceSystem))
	if _, ok := allowedCreditSources[source]; !ok {
		return apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "source_system is not allowed")
	}
	for name, value := range map[string]decimal.Decimal{"due_amount": due, "paid_amount": paid} {
		if value.Exponent() < -2 || value.GreaterThan(decimal.NewFromFloat(999999999999999.99)) {
			return apperror.New(400, "CRM_CREDIT_EVENT_INVALID", name+" exceeds the supported precision or range")
		}
	}
	for name, value := range map[string]string{"contract_no": event.ContractNo, "period_no": event.PeriodNo} {
		if value != "" && !creditReferencePattern.MatchString(value) {
			return apperror.New(400, "CRM_CREDIT_EVENT_INVALID", name+" has an invalid format")
		}
	}
	return nil
}

func samePaymentPayload(existing creditPaymentRecord, event PaymentEvent) bool {
	if existing.EventID != event.EventID || existing.PaymentID != event.PaymentID || existing.CustomerID != event.CustomerID ||
		existing.EventType != event.EventType || existing.ReversalOfPaymentID != event.ReversalOfPaymentID ||
		existing.ContractNo != event.ContractNo || existing.PeriodNo != event.PeriodNo || existing.SourceSystem != event.SourceSystem ||
		existing.DueAmount != event.DueAmount || existing.PaidAmount != event.PaidAmount {
		return false
	}
	if existing.DueDate == nil || !existing.DueDate.Equal(event.DueDate) {
		return false
	}
	if existing.PaidDate == nil || event.PaidDate == nil {
		return existing.PaidDate == nil && event.PaidDate == nil
	}
	return existing.PaidDate.Equal(*event.PaidDate)
}

func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func validRuleSettings(v RuleSettings) bool {
	return v.GraceDays >= 0 && v.GraceDays <= 90 && v.OnTimeThreshold >= 1 && v.OnTimeThreshold <= 100 && v.LateThreshold >= 1 && v.LateThreshold <= 100 && v.LevelStep >= 1 && v.LevelStep <= 3
}

func (s *Service) RuleSettings(ctx context.Context) (RuleSettings, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return RuleSettings{}, apperror.ErrUnauthenticated
	}
	value := defaultRuleSettings(p.TenantID, s.now())
	err := database.FromContext(ctx, s.db).Where("tenant_id=?", p.TenantID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return value, nil
	}
	return value, err
}

func (s *Service) UpdateRuleSettings(ctx context.Context, in UpdateRuleSettingsRequest) (RuleSettings, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return RuleSettings{}, apperror.ErrUnauthenticated
	}
	value := RuleSettings{TenantID: p.TenantID, GraceDays: in.GraceDays, OnTimeThreshold: in.OnTimeThreshold, LateThreshold: in.LateThreshold, LevelStep: in.LevelStep, Enabled: in.Enabled, UpdatedAt: s.now()}
	if !validRuleSettings(value) {
		return RuleSettings{}, apperror.New(400, "CRM_CREDIT_RULE_INVALID", "credit rule settings are invalid")
	}
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		var current RuleSettings
		lookupErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=?", p.TenantID).Take(&current).Error
		if lookupErr == nil {
			if in.UpdatedAt == nil || !current.UpdatedAt.Equal(*in.UpdatedAt) {
				return apperror.New(409, "CRM_CREDIT_RULE_VERSION_CONFLICT", "credit rule settings changed; refresh and retry")
			}
			if err := tx.Model(&RuleSettings{}).Where("tenant_id=? AND updated_at=?", p.TenantID, current.UpdatedAt).Updates(map[string]any{
				"grace_days": value.GraceDays, "on_time_threshold": value.OnTimeThreshold, "late_threshold": value.LateThreshold,
				"level_step": value.LevelStep, "enabled": value.Enabled, "updated_at": value.UpdatedAt,
			}).Error; err != nil {
				return err
			}
		} else if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			if err := tx.Create(&value).Error; err != nil {
				if isDuplicateKeyError(err) {
					return apperror.New(409, "CRM_CREDIT_RULE_VERSION_CONFLICT", "credit rule settings changed; refresh and retry")
				}
				return err
			}
		} else {
			return lookupErr
		}
		return tx.Create(&RuleSettingsVersion{TenantID: p.TenantID, GraceDays: value.GraceDays, OnTimeThreshold: value.OnTimeThreshold, LateThreshold: value.LateThreshold, LevelStep: value.LevelStep, Enabled: value.Enabled, ChangedBy: p.UserID, Reason: strings.TrimSpace(in.Reason), ChangedAt: value.UpdatedAt}).Error
	})
	return value, err
}

func ruleSettingsFor(tx *gorm.DB, tenant string, now time.Time) (RuleSettings, error) {
	value := defaultRuleSettings(tenant, now)
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=?", tenant).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return value, nil
	}
	return value, err
}

type creditState struct {
	Level  string
	OnTime uint32
	Late   uint32
	Last   *time.Time
}

func stateOf(customer struct {
	ID           uint64
	Level        string
	OnTime, Late uint32
	Last         *time.Time
}) creditState {
	return creditState{Level: customer.Level, OnTime: customer.OnTime, Late: customer.Late, Last: customer.Last}
}

func stateFromRecord(level *string, onTime, late *uint32, last *time.Time) (creditState, bool) {
	if level == nil || onTime == nil || late == nil {
		return creditState{}, false
	}
	return creditState{Level: *level, OnTime: *onTime, Late: *late, Last: last}, true
}

func sameState(left, right creditState) bool {
	if left.Level != right.Level || left.OnTime != right.OnTime || left.Late != right.Late {
		return false
	}
	if left.Last == nil || right.Last == nil {
		return left.Last == nil && right.Last == nil
	}
	return left.Last.Equal(*right.Last)
}

func (s *Service) ProcessPayment(ctx context.Context, event PaymentEvent) (PaymentRecord, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return PaymentRecord{}, apperror.ErrUnauthenticated
	}
	event.EventID = strings.TrimSpace(event.EventID)
	event.PaymentID = strings.TrimSpace(event.PaymentID)
	event.EventType = strings.ToLower(strings.TrimSpace(event.EventType))
	if event.EventType == "" {
		event.EventType = PaymentEventCompleted
	}
	if event.EventType != PaymentEventCompleted && event.EventType != PaymentEventReversed {
		return PaymentRecord{}, apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "event_type is not supported")
	}
	event.ReversalOfPaymentID = strings.TrimSpace(event.ReversalOfPaymentID)
	event.ContractNo = strings.TrimSpace(event.ContractNo)
	event.PeriodNo = strings.TrimSpace(event.PeriodNo)
	event.SourceSystem = strings.ToLower(strings.TrimSpace(event.SourceSystem))
	due, err := decimal.NewFromString(event.DueAmount)
	if err != nil || due.IsNegative() {
		return PaymentRecord{}, apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "due_amount is invalid")
	}
	paid, err := decimal.NewFromString(event.PaidAmount)
	if err != nil || paid.IsNegative() {
		return PaymentRecord{}, apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "paid_amount is invalid")
	}
	if err := validatePaymentEvent(event, due, paid); err != nil {
		return PaymentRecord{}, err
	}
	if event.EventType == PaymentEventReversed {
		return s.processReversal(ctx, p, event, paid)
	}
	now := s.now()
	var result PaymentRecord
	err = database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		rules, ruleErr := ruleSettingsFor(tx, p.TenantID, now)
		if ruleErr != nil {
			return ruleErr
		}
		var existing creditPaymentRecord
		if err := tx.Where("tenant_id=? AND (event_id=? OR payment_id=?)", p.TenantID, event.EventID, event.PaymentID).First(&existing).Error; err == nil {
			if !samePaymentPayload(existing, event) {
				return apperror.New(409, "CRM_CREDIT_EVENT_IDEMPOTENCY_CONFLICT", "event id or payment id is already bound to different payment data")
			}
			result = toPaymentRecord(existing)
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var customer struct {
			ID           uint64
			Level        string
			OnTime, Late uint32
			Last         *time.Time
		}
		if err := tx.Table("crm_customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,credit_level AS level,consecutive_ontime_count AS on_time,consecutive_late_count AS late,last_payment_eval_at AS last").Where("tenant_id=? AND id=? AND status='ACTIVE' AND merged_into_id IS NULL AND deleted_at IS NULL", p.TenantID, event.CustomerID).Take(&customer).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return s.insertRecord(tx, p.TenantID, event, "IGNORED_CUSTOMER_UNAVAILABLE", "CUSTOMER_UNAVAILABLE", rules.GraceDays, now, nil, nil, &result)
			}
			return err
		}
		previous := stateOf(customer)
		if event.PaidDate != nil && customer.Last != nil && event.PaidDate.Before(*customer.Last) {
			return s.insertRecord(tx, p.TenantID, event, "IGNORED_OUT_OF_ORDER", "OUT_OF_ORDER", rules.GraceDays, now, &previous, &previous, &result)
		}
		if !rules.Enabled {
			return s.insertRecord(tx, p.TenantID, event, "IGNORED_RULE_DISABLED", "RULE_DISABLED", rules.GraceDays, now, &previous, &previous, &result)
		}
		evaluation := "IGNORED_INCOMPLETE"
		ignoreReason := "INCOMPLETE_PAYMENT"
		if event.PaidDate != nil && paid.GreaterThanOrEqual(due) {
			ignoreReason = ""
			evaluation = paymentEvaluation(event.DueDate, *event.PaidDate, rules.GraceDays)
		}
		if evaluation == "IGNORED_INCOMPLETE" {
			return s.insertRecord(tx, p.TenantID, event, evaluation, ignoreReason, rules.GraceDays, now, &previous, &previous, &result)
		}
		next := previous
		if evaluation == "ONTIME" {
			next.OnTime++
			next.Late = 0
		} else {
			next.Late++
			next.OnTime = 0
		}
		next.Level = customer.Level
		ruleHit := false
		if evaluation == "ONTIME" && int(next.OnTime) >= rules.OnTimeThreshold {
			next.Level = stepLevel(customer.Level, -rules.LevelStep)
			next.OnTime = 0
			ruleHit = true
		}
		if evaluation == "LATE" && int(next.Late) >= rules.LateThreshold {
			next.Level = stepLevel(customer.Level, rules.LevelStep)
			next.Late = 0
			ruleHit = true
		}
		next.Last = event.PaidDate
		if err := s.insertRecord(tx, p.TenantID, event, evaluation, ignoreReason, rules.GraceDays, now, &previous, &next, &result); err != nil {
			return err
		}
		updates := map[string]any{"consecutive_ontime_count": next.OnTime, "consecutive_late_count": next.Late, "last_payment_eval_at": next.Last}
		if ruleHit {
			reason := evaluation
			logSource := "RULE"
			if next.Level == customer.Level {
				logSource, reason = "RULE_CAP", evaluation+"_CAP_REACHED"
			}
			updates["credit_level"] = next.Level
			if next.Level != customer.Level {
				updates["credit_updated_at"], updates["credit_change_source"] = now, "RULE"
			}
			if err := tx.Create(&CreditLog{TenantID: p.TenantID, CustomerID: event.CustomerID, FromLevel: customer.Level, ToLevel: next.Level, Source: logSource, Reason: reason, EventID: event.EventID, PaymentID: event.PaymentID, OperatorID: "system", OccurredAt: now}).Error; err != nil {
				return err
			}
			if err := s.writeCustomerChangeLog(tx, p.TenantID, event.CustomerID, customer.Level, next.Level, logSource, reason, "system", now); err != nil {
				return err
			}
			if next.Level != customer.Level {
				if err := s.notifyRuleChange(tx, p.TenantID, event.CustomerID, customer.Level, next.Level, now); err != nil {
					return err
				}
			}
		}
		return tx.Table("crm_customers").Where("tenant_id=? AND id=?", p.TenantID, event.CustomerID).Updates(updates).Error
	})
	return result, err
}

func (s *Service) processReversal(ctx context.Context, p auth.Principal, event PaymentEvent, paid decimal.Decimal) (PaymentRecord, error) {
	if paid.Sign() != 0 {
		return PaymentRecord{}, apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "reversal paid_amount must be zero")
	}
	now := s.now()
	var result PaymentRecord
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		rules, err := ruleSettingsFor(tx, p.TenantID, now)
		if err != nil {
			return err
		}
		var existing creditPaymentRecord
		if err = tx.Where("tenant_id=? AND (event_id=? OR payment_id=?)", p.TenantID, event.EventID, event.PaymentID).First(&existing).Error; err == nil {
			if !samePaymentPayload(existing, event) {
				return apperror.New(409, "CRM_CREDIT_EVENT_IDEMPOTENCY_CONFLICT", "event id or payment id is already bound to different payment data")
			}
			result = toPaymentRecord(existing)
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var original creditPaymentRecord
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND payment_id=?", p.TenantID, event.ReversalOfPaymentID).Take(&original).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperror.New(404, "CRM_CREDIT_PAYMENT_NOT_FOUND", "original credit payment was not found")
			}
			return err
		}
		if original.Evaluation != "ONTIME" && original.Evaluation != "LATE" {
			return apperror.New(409, "CRM_CREDIT_REVERSAL_UNSUPPORTED", "only an evaluated payment can be reversed")
		}
		if original.ReversedAt != nil {
			return apperror.New(409, "CRM_CREDIT_PAYMENT_ALREADY_REVERSED", "credit payment was already reversed")
		}
		if original.ResultLevel == nil || original.PrevLevel == nil || original.ResultOntimeCount == nil || original.ResultLateCount == nil || original.PrevOntimeCount == nil || original.PrevLateCount == nil {
			return apperror.New(409, "CRM_CREDIT_REVERSAL_UNSUPPORTED", "credit payment has no reversal snapshot")
		}
		var customer struct {
			ID           uint64
			Level        string
			OnTime, Late uint32
			Last         *time.Time
		}
		if err = tx.Table("crm_customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,credit_level AS level,consecutive_ontime_count AS on_time,consecutive_late_count AS late,last_payment_eval_at AS last").Where("tenant_id=? AND id=? AND status='ACTIVE' AND merged_into_id IS NULL AND deleted_at IS NULL", p.TenantID, event.CustomerID).Take(&customer).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
			}
			return err
		}
		var later int64
		if err = tx.Model(&creditPaymentRecord{}).Where("tenant_id=? AND customer_id=? AND id>?", p.TenantID, event.CustomerID, original.ID).Count(&later).Error; err != nil {
			return err
		}
		if later != 0 {
			return apperror.New(409, "CRM_CREDIT_REVERSAL_NOT_LATEST", "only the latest credit payment can be reversed")
		}
		current := stateOf(customer)
		applied := creditState{Level: *original.ResultLevel, OnTime: *original.ResultOntimeCount, Late: *original.ResultLateCount, Last: original.PaidDate}
		if !sameState(current, applied) {
			return apperror.New(409, "CRM_CREDIT_REVERSAL_STATE_CHANGED", "credit state changed after the payment; manual review is required")
		}
		restored := creditState{Level: *original.PrevLevel, OnTime: *original.PrevOntimeCount, Late: *original.PrevLateCount, Last: original.PrevLastEvalAt}
		if err = tx.Model(&creditPaymentRecord{}).Where("tenant_id=? AND id=? AND reversed_at IS NULL", p.TenantID, original.ID).Updates(map[string]any{"reversed_at": now, "reversal_event_id": event.EventID}).Error; err != nil {
			return err
		}
		reversal := event
		reversal.EventType = PaymentEventReversed
		reversal.ReversalOfPaymentID = original.PaymentID
		if err = s.insertRecord(tx, p.TenantID, reversal, "REVERSED", "PAYMENT_REVERSED", rules.GraceDays, now, &current, &restored, &result); err != nil {
			return err
		}
		if err = tx.Create(&CreditLog{TenantID: p.TenantID, CustomerID: event.CustomerID, FromLevel: current.Level, ToLevel: restored.Level, Source: "REVERSAL", Reason: "PAYMENT_REVERSED", EventID: event.EventID, PaymentID: event.PaymentID, OperatorID: "system", OccurredAt: now}).Error; err != nil {
			return err
		}
		if err = s.writeCustomerChangeLog(tx, p.TenantID, event.CustomerID, current.Level, restored.Level, "REVERSAL", "PAYMENT_REVERSED", "system", now); err != nil {
			return err
		}
		if restored.Level != current.Level {
			if err = s.notifyRuleChange(tx, p.TenantID, event.CustomerID, current.Level, restored.Level, now); err != nil {
				return err
			}
		}
		updates := map[string]any{"credit_level": restored.Level, "consecutive_ontime_count": restored.OnTime, "consecutive_late_count": restored.Late, "last_payment_eval_at": restored.Last, "credit_updated_at": now, "credit_change_source": "REVERSAL"}
		return tx.Table("crm_customers").Where("tenant_id=? AND id=?", p.TenantID, event.CustomerID).Updates(updates).Error
	})
	return result, err
}

func (s *Service) insertRecord(tx *gorm.DB, tenant string, event PaymentEvent, evaluation, ignoreReason string, grace int, now time.Time, previous, next *creditState, result *PaymentRecord) error {
	r := creditPaymentRecord{TenantID: tenant, CustomerID: event.CustomerID, EventID: event.EventID, PaymentID: event.PaymentID, EventType: event.EventType, ReversalOfPaymentID: event.ReversalOfPaymentID, ContractNo: event.ContractNo, PeriodNo: event.PeriodNo, SourceSystem: event.SourceSystem, DueDate: &event.DueDate, PaidDate: event.PaidDate, DueAmount: event.DueAmount, PaidAmount: event.PaidAmount, Evaluation: evaluation, IgnoreReason: ignoreReason, GraceDays: grace, EvaluatedAt: now, CreatedAt: now}
	if previous != nil {
		r.PrevLevel, r.PrevOntimeCount, r.PrevLateCount, r.PrevLastEvalAt = &previous.Level, &previous.OnTime, &previous.Late, previous.Last
	}
	if next != nil {
		r.ResultLevel, r.ResultOntimeCount, r.ResultLateCount = &next.Level, &next.OnTime, &next.Late
	}
	if err := tx.Create(&r).Error; err != nil {
		return err
	}
	*result = toPaymentRecord(r)
	return nil
}
func toPaymentRecord(r creditPaymentRecord) PaymentRecord {
	return PaymentRecord{ID: r.ID, EventID: r.EventID, PaymentID: r.PaymentID, EventType: r.EventType, ReversalOfPaymentID: r.ReversalOfPaymentID, Evaluation: r.Evaluation, IgnoreReason: r.IgnoreReason, DueDate: r.DueDate, PaidDate: r.PaidDate, DueAmount: r.DueAmount, PaidAmount: r.PaidAmount, ContractNo: r.ContractNo, PeriodNo: r.PeriodNo, SourceSystem: r.SourceSystem, GraceDays: r.GraceDays, EvaluatedAt: r.EvaluatedAt, ReversedAt: r.ReversedAt, ReversalEventID: r.ReversalEventID}
}
func stepLevel(level string, delta int) string {
	i := map[string]int{"A": 0, "B": 1, "C": 2, "D": 3}[level] + delta
	if i < 0 {
		i = 0
	}
	if i > 3 {
		i = 3
	}
	return []string{"A", "B", "C", "D"}[i]
}

func validLevel(level string) bool {
	return level == LevelA || level == LevelB || level == LevelC || level == LevelD
}

func (s *Service) Apply(ctx context.Context, customerID uint64, in ApplyRequest) (*Application, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrUnauthenticated
	}
	if !p.HasPermission("customer.credit.apply") || !creditSalesOnlyPrincipal(p) {
		return nil, apperror.ErrForbidden
	}
	// 原因用于审批和审计，去除首尾空格后至少保留两个字符，
	// 避免单字符或纯空白内容形成不可追溯的调整记录。
	in.Reason = strings.TrimSpace(in.Reason)
	if !validLevel(in.TargetLevel) || len([]rune(in.Reason)) < 2 || len([]rune(in.Reason)) > 500 {
		return nil, apperror.New(400, "CRM_CREDIT_APPLICATION_INVALID", "target level or reason is invalid")
	}
	now := s.now()
	idempotencyKey := strings.TrimSpace(in.IdempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, apperror.New(400, "CRM_CREDIT_APPLICATION_IDEMPOTENCY_REQUIRED", "Idempotency-Key is required")
	}
	var out Application
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		if idempotencyKey != "" {
			var replay Application
			if err := tx.Where("tenant_id=? AND applicant_id=? AND idempotency_key=?", p.TenantID, p.UserID, idempotencyKey).Take(&replay).Error; err == nil {
				if replay.CustomerID != customerID || replay.TargetLevel != in.TargetLevel || replay.Reason != in.Reason {
					return apperror.New(409, "CRM_CREDIT_APPLICATION_IDEMPOTENCY_CONFLICT", "idempotency key is already bound to another application")
				}
				out = replay
				return nil
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		var customer struct {
			ID          uint64
			CreditLevel string
		}
		customerErr := scopedCreditCustomers(tx, p).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("crm_customers.id,crm_customers.credit_level").
			Where("crm_customers.id=? AND crm_customers.status='ACTIVE' AND crm_customers.merged_into_id IS NULL", customerID).
			Take(&customer).Error
		if errors.Is(customerErr, gorm.ErrRecordNotFound) {
			return apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
		}
		if customerErr != nil {
			return customerErr
		}
		if customer.CreditLevel == in.TargetLevel {
			return apperror.New(409, "CRM_CREDIT_APPLICATION_SAME_LEVEL", "target level equals current level")
		}
		pending := customerID
		out = Application{TenantID: p.TenantID, CustomerID: customerID, ApplicantID: p.UserID, IdempotencyKey: idempotencyKey, FromLevel: customer.CreditLevel, TargetLevel: in.TargetLevel, Reason: in.Reason, Status: "PENDING", CreatedAt: now, UpdatedAt: now, Version: 1, PendingCustomerID: &pending}
		if err := tx.Create(&out).Error; err != nil {
			if isDuplicateKeyError(err) {
				return apperror.New(409, "CRM_CREDIT_APPLICATION_PENDING_EXISTS", "a pending credit application already exists")
			}
			return err
		}
		instance := ApprovalInstance{TenantID: p.TenantID, BizType: "CREDIT_ADJUSTMENT", BusinessID: out.ID, Status: "PENDING", CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now, Version: 1}
		if err := tx.Create(&instance).Error; err != nil {
			return err
		}
		task := ApprovalTask{TenantID: p.TenantID, InstanceID: instance.ID, TaskCode: "SALES_DIRECTOR_APPROVE", AssigneeRole: "sales_director", Status: "PENDING", CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		if err := tx.Model(&ApprovalInstance{}).Where("tenant_id=? AND id=?", p.TenantID, instance.ID).Update("current_task_id", task.ID).Error; err != nil {
			return err
		}
		out.ApprovalInstanceID = instance.ID
		if err := tx.Model(&Application{}).Where("tenant_id=? AND id=?", p.TenantID, out.ID).Update("approval_instance_id", instance.ID).Error; err != nil {
			return err
		}
		return s.notifySalesDirectors(tx, p.TenantID, customerID, out.ID, "CREDIT_APPLICATION_PENDING", "信用等级调整待审批", "客户信用等级调整申请等待审批。", now)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Decide 用途：在申请和客户锁内按数据范围审批客户信用调整。
// 参数：ctx 为请求；applicationID 为申请；approve 为决定；in 为意见与版本。
// 返回值：审批结果；无权限、不可见、状态冲突或存储失败返回错误。
func (s *Service) Decide(ctx context.Context, applicationID uint64, approve bool, in DecisionRequest) (*Application, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrUnauthenticated
	}
	if !auth.HasPermissionOrRole(p, "customer.credit.approve", "sales_director", "crm_super_admin") {
		return nil, apperror.ErrForbidden
	}
	in.Opinion = strings.TrimSpace(in.Opinion)
	if len([]rune(in.Opinion)) > 500 {
		return nil, apperror.New(400, "CRM_CREDIT_OPINION_TOO_LONG", "credit opinion exceeds 500 characters")
	}
	if !approve && len([]rune(in.Opinion)) < 2 {
		return nil, apperror.New(400, "CRM_CREDIT_REJECTION_OPINION_REQUIRED", "rejection opinion is required")
	}
	now := s.now()
	var out Application
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		// 申请行本身没有归属列，范围在客户上。先做一次非锁定读取拿到 customer_id，
		// 按客户数据范围判定可见性（与 Apply 的客户范围同口径）；不可见时统一回
		// "申请不存在"，避免审批人通过错误码探测范围外客户/申请是否存在。
		var target struct{ CustomerID uint64 }
		if err := tx.Table("crm_customer_credit_applications").Select("customer_id").Where("tenant_id=? AND id=?", p.TenantID, applicationID).Take(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return creditApplicationNotFound()
			}
			return err
		}
		if err := ensureCreditApplicationCustomerVisible(tx, p, target.CustomerID); err != nil {
			return err
		}
		// 取得可见性结论后才锁定申请行，锁序仍保持“申请→客户”，与 Apply 一致。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", p.TenantID, applicationID).Take(&out).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return creditApplicationNotFound()
			}
			return err
		}
		if out.Status != "PENDING" {
			return apperror.New(409, "CRM_CREDIT_APPLICATION_NOT_PENDING", "credit application is not pending")
		}
		if in.Version == 0 || in.Version != out.Version {
			return apperror.New(409, "CRM_CREDIT_APPLICATION_VERSION_CONFLICT", "credit application has changed; refresh and retry")
		}
		var customer struct {
			ID          uint64
			CreditLevel string
		}
		// 客户锁同样必须落在主体可见范围内：范围在可见性校验与加锁之间发生变化时，
		// 锁查询会失败而不是继续审批范围外客户的信用等级。失败按既有契约回 409
		// （客户已失效/不可用）：调用方此前已通过 404 可见性闸门，失败不额外泄露存在性。
		if err := lockScopedCreditCustomer(tx, p, out.CustomerID, &customer); err != nil {
			return apperror.New(409, "CRM_CREDIT_APPLICATION_INVALIDATED", "customer is unavailable")
		}
		status := "REJECTED"
		if approve {
			if customer.CreditLevel != out.FromLevel {
				status = "INVALIDATED"
			} else {
				status = "APPROVED"
				if err := tx.Table("crm_customers").Where("tenant_id=? AND id=?", p.TenantID, out.CustomerID).Updates(map[string]any{"credit_level": out.TargetLevel, "credit_updated_at": now, "credit_change_source": "MANUAL", "consecutive_ontime_count": 0, "consecutive_late_count": 0}).Error; err != nil {
					return err
				}
				if err := tx.Create(&CreditLog{TenantID: p.TenantID, CustomerID: out.CustomerID, ApplicationID: out.ID, FromLevel: out.FromLevel, ToLevel: out.TargetLevel, Source: "MANUAL", Reason: in.Opinion, OperatorID: p.UserID, OccurredAt: now}).Error; err != nil {
					return err
				}
			}
		}
		out.Status = status
		out.Opinion = in.Opinion
		out.DecidedBy = p.UserID
		out.DecidedAt = &now
		out.PendingCustomerID = nil
		out.UpdatedAt = now
		out.Version++
		if err := tx.Save(&out).Error; err != nil {
			return err
		}
		approvalStatus := status
		if err := tx.Model(&ApprovalTask{}).Where("tenant_id=? AND instance_id=? AND status='PENDING'", p.TenantID, out.ApprovalInstanceID).Updates(map[string]any{"status": approvalStatus, "decided_by": p.UserID, "decided_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&ApprovalInstance{}).Where("tenant_id=? AND id=?", p.TenantID, out.ApprovalInstanceID).Updates(map[string]any{"status": approvalStatus, "decided_by": p.UserID, "decided_at": now, "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
		if status == "APPROVED" {
			if err := s.writeCustomerChangeLog(tx, p.TenantID, out.CustomerID, out.FromLevel, out.TargetLevel, "MANUAL", in.Opinion, p.UserID, now); err != nil {
				return err
			}
		}
		kind, title, body := "CREDIT_APPLICATION_REJECTED", "信用等级调整已驳回", "您的信用等级调整申请已被驳回。"
		if status == "APPROVED" {
			kind, title, body = "CREDIT_APPLICATION_APPROVED", "信用等级调整已通过", "您的信用等级调整申请已通过，客户信用等级已更新。"
		}
		if status == "INVALIDATED" {
			kind, title, body = "CREDIT_APPLICATION_INVALIDATED", "信用等级调整已失效", "客户信用等级已被规则更新，请重新评估后发起申请。"
		}
		return s.notifyApplication(tx, p.TenantID, out.CustomerID, out.ID, out.ApplicantID, kind, title, body, now)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Service) Withdraw(ctx context.Context, customerID, applicationID uint64) (*Application, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrUnauthenticated
	}
	if !p.HasPermission("customer.credit.apply") || !creditSalesOnlyPrincipal(p) {
		return nil, apperror.ErrForbidden
	}
	var out Application
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=? AND customer_id=? AND applicant_id=?", p.TenantID, applicationID, customerID, p.UserID).Take(&out).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperror.New(404, "CRM_CREDIT_APPLICATION_NOT_FOUND", "credit application not found")
			}
			return err
		}
		if out.Status != "PENDING" {
			return apperror.New(409, "CRM_CREDIT_APPLICATION_NOT_PENDING", "credit application is not pending")
		}
		out.Status = "WITHDRAWN"
		out.PendingCustomerID = nil
		out.UpdatedAt = s.now()
		out.Version++
		if err := tx.Save(&out).Error; err != nil {
			return err
		}
		if err := tx.Model(&ApprovalTask{}).Where("tenant_id=? AND instance_id=? AND status='PENDING'", p.TenantID, out.ApprovalInstanceID).Update("status", "WITHDRAWN").Error; err != nil {
			return err
		}
		return tx.Model(&ApprovalInstance{}).Where("tenant_id=? AND id=?", p.TenantID, out.ApprovalInstanceID).Updates(map[string]any{"status": "WITHDRAWN", "decided_by": p.UserID, "decided_at": out.UpdatedAt, "updated_at": out.UpdatedAt, "version": gorm.Expr("version+1")}).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Pending 用途：通过客户可见性子查询读取当前审批人的信用待办。
// 参数：ctx 为携带认证主体的请求上下文。
// 返回值：可见待办列表；认证、权限或查询失败返回错误。
func (s *Service) Pending(ctx context.Context) ([]PendingApplication, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrUnauthenticated
	}
	if !auth.HasPermissionOrRole(p, "customer.credit.approve", "sales_director", "crm_super_admin") {
		return nil, apperror.ErrForbidden
	}
	// 待办箱为空时保持集合结构稳定。nil 切片会
	// 序列化为 JSON null，导致客户端在全部待办处理完毕时
	// 将响应对象误认为条目列表。
	items := make([]PendingApplication, 0)
	db := database.FromContext(ctx, s.db)
	// 审批待办必须与客户主数据同口径：只返回当前主体可见客户的申请，否则审批人能通过
	// 待办事由、客户名称读到数据范围外的客户。join 已把 crm_customers 命名为 customer 别名，
	// 而范围谓词使用 crm_customers 全限定列名，因此必须用独立子查询，不能直接套在 join 上。
	visibleCustomers := scopedCreditCustomers(db.Session(&gorm.Session{NewDB: true}), p).Select("crm_customers.id")
	err := db.
		Table("crm_customer_credit_applications AS application").
		Select("application.id, application.customer_id, customer.customer_no, customer.name AS customer_name, application.applicant_id, application.from_level, application.target_level, application.reason, application.status, application.approval_instance_id, application.created_at, application.updated_at, application.version").
		Joins("LEFT JOIN crm_customers AS customer ON customer.tenant_id = application.tenant_id AND customer.id = application.customer_id").
		Where("application.tenant_id=? AND application.status='PENDING'", p.TenantID).
		Where("application.customer_id IN (?)", visibleCustomers).
		Order("application.created_at ASC").
		Scan(&items).Error
	return items, err
}
func (s *Service) History(ctx context.Context, customerID uint64, page, pageSize int) (pagination.Page[CreditLog], error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return pagination.Page[CreditLog]{}, apperror.ErrUnauthenticated
	}
	if err := ensureCreditCustomerVisible(database.FromContext(ctx, s.db), p, customerID); err != nil {
		return pagination.Page[CreditLog]{}, err
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	db := database.FromContext(ctx, s.db)
	visibleCustomer := scopedCreditCustomers(db.Session(&gorm.Session{NewDB: true}), p).
		Select("crm_customers.id").
		Where("crm_customers.id=?", customerID)
	// 将可见性子查询保留在实际数据读取中，避免客户组织归属在授权检查与分页查询之间变化时
	// 泄露已经移出当前范围的信用历史。
	query := db.Where("tenant_id=? AND customer_id=? AND customer_id IN (?)", p.TenantID, customerID, visibleCustomer)
	var total int64
	if err := query.Model(&CreditLog{}).Count(&total).Error; err != nil {
		return pagination.Page[CreditLog]{}, err
	}
	var items []CreditLog
	err := query.Order("occurred_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	return pagination.Page[CreditLog]{Items: items, Page: page, PageSize: pageSize, Total: total}, err
}
func (s *Service) Payments(ctx context.Context, customerID uint64, page, pageSize int) (pagination.Page[PaymentRecord], error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return pagination.Page[PaymentRecord]{}, apperror.ErrUnauthenticated
	}
	if err := ensureCreditCustomerVisible(database.FromContext(ctx, s.db), p, customerID); err != nil {
		return pagination.Page[PaymentRecord]{}, err
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	db := database.FromContext(ctx, s.db)
	visibleCustomer := scopedCreditCustomers(db.Session(&gorm.Session{NewDB: true}), p).
		Select("crm_customers.id").
		Where("crm_customers.id=?", customerID)
	query := db.Where("tenant_id=? AND customer_id=? AND customer_id IN (?)", p.TenantID, customerID, visibleCustomer)
	var total int64
	if err := query.Model(&creditPaymentRecord{}).Count(&total).Error; err != nil {
		return pagination.Page[PaymentRecord]{}, err
	}
	var rows []creditPaymentRecord
	err := query.Order("evaluated_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	items := make([]PaymentRecord, 0, len(rows))
	for _, r := range rows {
		items = append(items, toPaymentRecord(r))
	}
	return pagination.Page[PaymentRecord]{Items: items, Page: page, PageSize: pageSize, Total: total}, err
}
func (s *Service) writeCustomerChangeLog(tx *gorm.DB, tenant string, customerID uint64, from, to, source, reason, operator string, now time.Time) error {
	return tx.Table("crm_customer_change_logs").Create(map[string]any{
		"tenant_id": tenant, "customer_id": customerID, "field_name": "credit_level", "before_json": fmt.Sprintf(`"%s"`, from), "after_json": fmt.Sprintf(`"%s"`, to),
		"reason": reason, "operator_id": operator, "request_id": request.ID(tx.Statement.Context), "occurred_at": now,
	}).Error
}

func (s *Service) notifyRuleChange(tx *gorm.DB, tenant string, customerID uint64, from, to string, now time.Time) error {
	var customer struct{ OwnerUserID string }
	if err := tx.Table("crm_customers").Select("owner_user_id").Where("tenant_id=? AND id=?", tenant, customerID).Take(&customer).Error; err != nil {
		return err
	}
	if customer.OwnerUserID != "" {
		if err := s.notifyApplication(tx, tenant, customerID, 0, customer.OwnerUserID, "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", fmt.Sprintf("客户信用等级已由规则从 %s 调整为 %s。", from, to), now); err != nil {
			return err
		}
	}
	return s.notifySalesDirectors(tx, tenant, customerID, 0, "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", fmt.Sprintf("客户信用等级已由规则从 %s 调整为 %s。", from, to), now)
}

func (s *Service) notifySalesDirectors(tx *gorm.DB, tenant string, customerID, applicationID uint64, kind, title, body string, now time.Time) error {
	if s.owners == nil {
		return apperror.New(503, "CRM_CREDIT_OWNER_DIRECTORY_UNAVAILABLE", "owner directory is unavailable for credit notification")
	}
	seen := map[string]struct{}{}
	for pageNumber := 1; pageNumber <= 100; pageNumber++ {
		page, err := s.owners.List(tx.Statement.Context, ownerdirectory.Query{RoleCodes: []string{"sales_director"}, Page: pageNumber, PageSize: 50})
		if err != nil {
			return apperror.New(503, "CRM_CREDIT_OWNER_DIRECTORY_UNAVAILABLE", "owner directory is unavailable for credit notification")
		}
		for _, user := range page.Items {
			user.ID = strings.TrimSpace(user.ID)
			if user.ID == "" {
				continue
			}
			if _, exists := seen[user.ID]; exists {
				continue
			}
			seen[user.ID] = struct{}{}
			if err := s.notifyApplication(tx, tenant, customerID, applicationID, user.ID, kind, title, body, now); err != nil {
				return err
			}
		}
		if len(page.Items) == 0 || int64(len(seen)) >= page.Total || len(page.Items) < 50 {
			break
		}
	}
	if len(seen) == 0 {
		return apperror.New(503, "CRM_CREDIT_OWNER_DIRECTORY_UNAVAILABLE", "no sales director is available for credit notification")
	}
	return nil
}

// creditNotificationSourceID 把一次信用通知的完整语义折叠成稳定的
// source_event_id。crm_notifications 的唯一键是 (tenant_id, source_event_id)，
// 并配合 ON DUPLICATE KEY UPDATE id=id 做幂等写入：
//   - 同一档等级变化被事务重试时（body/收件人/类型完全相同）得到同一个键，
//     收件人只会看到一条通知；
//   - 不同 from/to 或其他正文内容、不同收件人、不同申请生成不同的键，
//     每次变化都各推一条，不会被上一次变化的键吞掉。
//
// 键中不包含时间：重试发生在事务回滚之后，时间必然不同，一旦参与摘要就会
// 把同一次变化重复写入站内信。摘要长度固定为 "credit:"+64 位十六进制，
// 满足 source_event_id 的列宽与平台事件码字符集。
func creditNotificationSourceID(tenant string, customerID, applicationID uint64, recipientID, kind, title, body string) string {
	// 使用不可能出现在正文中的分隔符拼接，避免不同字段拼接后发生歧义碰撞。
	material := strings.Join([]string{
		tenant,
		strconv.FormatUint(customerID, 10),
		strconv.FormatUint(applicationID, 10),
		kind,
		recipientID,
		title,
		body,
	}, "\x1f")
	sum := sha256.Sum256([]byte(material))
	return "credit:" + hex.EncodeToString(sum[:])
}

func (s *Service) notifyApplication(tx *gorm.DB, tenant string, customerID, applicationID uint64, recipientID, kind, title, body string, now time.Time) error {
	if strings.TrimSpace(recipientID) == "" {
		return nil
	}
	sourceID := creditNotificationSourceID(tenant, customerID, applicationID, recipientID, kind, title, body)
	return tx.Exec(`INSERT INTO crm_notifications (tenant_id,created_by,updated_by,source_event_id,type,opportunity_id,customer_id,opportunity_version,opportunity_no,opportunity_name,request_id,request_no,assignment_id,progress_id,recipient_id,recipient_kind,title,body,target_path,status,created_at,updated_at,version)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`,
		tenant, "system", "system", sourceID, kind, 0, customerID, 0, "", "", 0, "", 0, 0, recipientID, "USER", title, body,
		fmt.Sprintf("/customer-opportunity/customers?customer_id=%d&tab=credit", customerID), "UNREAD", now, now, 1).Error
}

// beijingCalendarDay 把时刻折算成北京日历日当天 00:00，用于按业务日粒度比较日期。
// 数据库中的时间戳仍然是 UTC，只有“业务日期”判断走北京日历。
func beijingCalendarDay(value time.Time) time.Time {
	year, month, day := value.In(businessclock.Location()).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, businessclock.Location())
}

// paymentEvaluation 判定付款是否在宽限期内完成。
//
// 到期日与付款日先各自折算成北京日历日，再比较日历日：宽限截止日当天仍判 ONTIME，
// 次日起判 LATE。旧实现以 UTC 午夜的 deadline 与付款时刻直接比较，既让宽限日当天
// 的付款几乎必然判成 LATE（deadline 落在当日 00:00），也把北京时间 00:00–08:00
// 的付款错算到 UTC 的前一天。
func paymentEvaluation(dueDate, paidDate time.Time, graceDays int) string {
	deadline := beijingCalendarDay(dueDate).AddDate(0, 0, graceDays)
	if !beijingCalendarDay(paidDate).After(deadline) {
		return "ONTIME"
	}
	return "LATE"
}
