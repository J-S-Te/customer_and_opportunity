package credit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}}, DoUpdates: clause.AssignmentColumns([]string{"grace_days", "on_time_threshold", "late_threshold", "level_step", "enabled", "updated_at"})}).Create(&value).Error; err != nil {
			return err
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

func (s *Service) ProcessPayment(ctx context.Context, event PaymentEvent) (PaymentRecord, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return PaymentRecord{}, apperror.ErrUnauthenticated
	}
	if event.EventID == "" || event.PaymentID == "" || event.CustomerID == 0 {
		return PaymentRecord{}, apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "event_id, payment_id and customer_id are required")
	}
	due, err := decimal.NewFromString(event.DueAmount)
	if err != nil || due.IsNegative() {
		return PaymentRecord{}, apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "due_amount is invalid")
	}
	paid, err := decimal.NewFromString(event.PaidAmount)
	if err != nil || paid.IsNegative() {
		return PaymentRecord{}, apperror.New(400, "CRM_CREDIT_EVENT_INVALID", "paid_amount is invalid")
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
		if err := tx.Table("crm_customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,credit_level,consecutive_ontime_count AS on_time,consecutive_late_count AS late,last_payment_eval_at AS last").Where("tenant_id=? AND id=? AND status='ACTIVE' AND merged_into_id IS NULL AND deleted_at IS NULL", p.TenantID, event.CustomerID).Take(&customer).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return s.insertRecord(tx, p.TenantID, event, "IGNORED_CUSTOMER_UNAVAILABLE", "CUSTOMER_UNAVAILABLE", rules.GraceDays, now, &result)
			}
			return err
		}
		if event.PaidDate != nil && customer.Last != nil && event.PaidDate.Before(*customer.Last) {
			return s.insertRecord(tx, p.TenantID, event, "IGNORED_OUT_OF_ORDER", "OUT_OF_ORDER", rules.GraceDays, now, &result)
		}
		if !rules.Enabled {
			return s.insertRecord(tx, p.TenantID, event, "IGNORED_RULE_DISABLED", "RULE_DISABLED", rules.GraceDays, now, &result)
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
		if err := s.insertRecord(tx, p.TenantID, event, evaluation, ignoreReason, rules.GraceDays, now, &result); err != nil {
			return err
		}
		if evaluation == "IGNORED_INCOMPLETE" {
			return nil
		}
		if evaluation == "ONTIME" {
			customer.OnTime++
			customer.Late = 0
		} else {
			customer.Late++
			customer.OnTime = 0
		}
		newLevel := customer.Level
		ruleHit := false
		if evaluation == "ONTIME" && int(customer.OnTime) >= rules.OnTimeThreshold {
			newLevel = stepLevel(customer.Level, -rules.LevelStep)
			customer.OnTime = 0
			ruleHit = true
		}
		if evaluation == "LATE" && int(customer.Late) >= rules.LateThreshold {
			newLevel = stepLevel(customer.Level, rules.LevelStep)
			customer.Late = 0
			ruleHit = true
		}
		updates := map[string]any{"consecutive_ontime_count": customer.OnTime, "consecutive_late_count": customer.Late, "last_payment_eval_at": event.PaidDate}
		if ruleHit {
			reason := evaluation
			logSource := "RULE"
			if newLevel == customer.Level {
				logSource, reason = "RULE_CAP", evaluation+"_CAP_REACHED"
			}
			updates["credit_level"] = newLevel
			if newLevel != customer.Level {
				updates["credit_updated_at"], updates["credit_change_source"] = now, "RULE"
			}
			if err := tx.Create(&CreditLog{TenantID: p.TenantID, CustomerID: event.CustomerID, FromLevel: customer.Level, ToLevel: newLevel, Source: logSource, Reason: reason, EventID: event.EventID, PaymentID: event.PaymentID, OperatorID: "system", OccurredAt: now}).Error; err != nil {
				return err
			}
			if err := s.writeCustomerChangeLog(tx, p.TenantID, event.CustomerID, customer.Level, newLevel, logSource, reason, "system", now); err != nil {
				return err
			}
			if newLevel != customer.Level {
				if err := s.notifyRuleChange(tx, p.TenantID, event.CustomerID, customer.Level, newLevel, now); err != nil {
					return err
				}
			}
		}
		return tx.Table("crm_customers").Where("tenant_id=? AND id=?", p.TenantID, event.CustomerID).Updates(updates).Error
	})
	return result, err
}

func (s *Service) insertRecord(tx *gorm.DB, tenant string, event PaymentEvent, evaluation, ignoreReason string, grace int, now time.Time, result *PaymentRecord) error {
	r := creditPaymentRecord{TenantID: tenant, CustomerID: event.CustomerID, EventID: event.EventID, PaymentID: event.PaymentID, ContractNo: event.ContractNo, PeriodNo: event.PeriodNo, SourceSystem: event.SourceSystem, DueDate: &event.DueDate, PaidDate: event.PaidDate, DueAmount: event.DueAmount, PaidAmount: event.PaidAmount, Evaluation: evaluation, IgnoreReason: ignoreReason, GraceDays: grace, EvaluatedAt: now, CreatedAt: now}
	if err := tx.Create(&r).Error; err != nil {
		return err
	}
	*result = toPaymentRecord(r)
	return nil
}
func toPaymentRecord(r creditPaymentRecord) PaymentRecord {
	return PaymentRecord{ID: r.ID, EventID: r.EventID, PaymentID: r.PaymentID, Evaluation: r.Evaluation, IgnoreReason: r.IgnoreReason, DueDate: r.DueDate, PaidDate: r.PaidDate, DueAmount: r.DueAmount, PaidAmount: r.PaidAmount, ContractNo: r.ContractNo, PeriodNo: r.PeriodNo, SourceSystem: r.SourceSystem, GraceDays: r.GraceDays, EvaluatedAt: r.EvaluatedAt}
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
	// 原因用于审批和审计，只要求有实际内容；前端允许简短中文说明，
	// 不能再使用旧的“至少 20 字”限制阻断合法的信用调整申请。
	in.Reason = strings.TrimSpace(in.Reason)
	if !validLevel(in.TargetLevel) || in.Reason == "" {
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
			return apperror.New(409, "CRM_CREDIT_APPLICATION_PENDING_EXISTS", "a pending credit application already exists")
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
	if !hasRole(p, "sales_director") {
		return nil, apperror.ErrForbidden
	}
	if !approve && len([]rune(in.Opinion)) == 0 {
		return nil, apperror.New(400, "CRM_CREDIT_REJECTION_OPINION_REQUIRED", "rejection opinion is required")
	}
	now := s.now()
	var out Application
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", p.TenantID, applicationID).Take(&out).Error; err != nil {
			return apperror.New(404, "CRM_CREDIT_APPLICATION_NOT_FOUND", "credit application not found")
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
	var out Application
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=? AND customer_id=?", p.TenantID, applicationID, customerID).Take(&out).Error; err != nil {
			return apperror.New(404, "CRM_CREDIT_APPLICATION_NOT_FOUND", "credit application not found")
		}
		if out.Status != "PENDING" || out.ApplicantID != p.UserID {
			return apperror.ErrForbidden
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

func (s *Service) Pending(ctx context.Context) ([]Application, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrUnauthenticated
	}
	if !p.HasPermission("customer.credit.approve") {
		return nil, apperror.ErrForbidden
	}
	var items []Application
	err := database.FromContext(ctx, s.db).Where("tenant_id=? AND status='PENDING'", p.TenantID).Order("created_at ASC").Find(&items).Error
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
func hasRole(p auth.Principal, want string) bool {
	for _, role := range p.Roles {
		if role == want {
			return true
		}
	}
	return false
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
