package credit

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
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
		Select("customer.id,customer.credit_level,customer.credit_change_source,customer.credit_updated_at AS updated_at,customer.consecutive_ontime_count AS on_time,customer.consecutive_late_count AS late").
		Where("customer.id=?", customerID).
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
		Select("COALESCE(SUM(customer.credit_level='C'),0) AS level_c,COALESCE(SUM(customer.credit_level='D'),0) AS level_d,COALESCE(SUM(customer.credit_level IN ('C','D')),0) AS attention_customers").
		Where("customer.status='ACTIVE' AND customer.merged_into_id IS NULL").
		Scan(&result).Error
	return result, err
}

// scopedCreditCustomers 将信用子模块收口到与客户主数据一致的数据范围。
// 销售角色即使获得平台 application/tenant 范围，也只能读取本人创建的客户；
// 管理角色继续遵循平台下发的 ALL/ORG 范围，未知或个人范围按负责人本人失败收窄。
func scopedCreditCustomers(db *gorm.DB, principal auth.Principal) *gorm.DB {
	db = db.Table("crm_customers AS customer").
		Where("customer.tenant_id=? AND customer.deleted_at IS NULL", principal.TenantID)
	if creditSalesOnlyPrincipal(principal) {
		return db.Where("customer.created_by=?", principal.UserID)
	}
	switch principal.ScopeMode {
	case auth.ScopeAll:
		return db
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return db.Where("1=0")
		}
		return db.Where("customer.owner_org_id IN ?", principal.OrganizationIDs)
	default:
		return db.Where("customer.owner_user_id=?", principal.UserID)
	}
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
		Select("customer.id").
		Where("customer.id=?", customerID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
	}
	return err
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
			deadline := event.DueDate.UTC().AddDate(0, 0, rules.GraceDays)
			if !event.PaidDate.After(deadline) {
				evaluation = "ONTIME"
			} else {
				evaluation = "LATE"
			}
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
			Select("customer.id,customer.credit_level").
			Where("customer.id=? AND customer.status='ACTIVE' AND customer.merged_into_id IS NULL", customerID).
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
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", p.TenantID, applicationID).Take(&out).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperror.New(404, "CRM_CREDIT_APPLICATION_NOT_FOUND", "credit application not found")
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
		if err := tx.Table("crm_customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,credit_level").Where("tenant_id=? AND id=? AND status='ACTIVE' AND merged_into_id IS NULL AND deleted_at IS NULL", p.TenantID, out.CustomerID).Take(&customer).Error; err != nil {
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
	err := database.FromContext(ctx, s.db).
		Table("crm_customer_credit_applications AS application").
		Select("application.id, application.customer_id, customer.customer_no, customer.name AS customer_name, application.applicant_id, application.from_level, application.target_level, application.reason, application.status, application.approval_instance_id, application.created_at, application.updated_at, application.version").
		Joins("LEFT JOIN crm_customers AS customer ON customer.tenant_id = application.tenant_id AND customer.id = application.customer_id").
		Where("application.tenant_id=? AND application.status='PENDING'", p.TenantID).
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
		Select("customer.id").
		Where("customer.id=?", customerID)
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
		Select("customer.id").
		Where("customer.id=?", customerID)
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

func (s *Service) notifyApplication(tx *gorm.DB, tenant string, customerID, applicationID uint64, recipientID, kind, title, body string, now time.Time) error {
	if strings.TrimSpace(recipientID) == "" {
		return nil
	}
	sourceID := fmt.Sprintf("credit:%s:%d:%d:%s:%s", tenant, customerID, applicationID, kind, recipientID)
	return tx.Exec(`INSERT INTO crm_notifications (tenant_id,created_by,updated_by,source_event_id,type,opportunity_id,customer_id,opportunity_version,opportunity_no,opportunity_name,request_id,request_no,assignment_id,progress_id,recipient_id,recipient_kind,title,body,target_path,status,created_at,updated_at,version)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`,
		tenant, "system", "system", sourceID, kind, 0, customerID, 0, "", "", 0, "", 0, 0, recipientID, "USER", title, body,
		fmt.Sprintf("/customer-opportunity/customers?customer_id=%d&tab=credit", customerID), "UNREAD", now, now, 1).Error
}
