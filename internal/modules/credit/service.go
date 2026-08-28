package credit

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db                   *gorm.DB
	graceDays, threshold int
	now                  func() time.Time
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, graceDays: 7, threshold: 2, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) GetLevel(ctx context.Context, customerID uint64) (LevelResponse, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return LevelResponse{}, apperror.ErrUnauthenticated
	}
	var row struct {
		ID                  uint64
		CreditLevel, Source string
		UpdatedAt           *time.Time
	}
	err := database.FromContext(ctx, s.db).Table("crm_customers").Select("id,credit_level,credit_change_source,credit_updated_at AS updated_at").Where("tenant_id=? AND id=? AND deleted_at IS NULL", p.TenantID, customerID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return LevelResponse{}, apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
	}
	if err != nil {
		return LevelResponse{}, err
	}
	return LevelResponse{CustomerID: row.ID, Level: row.CreditLevel, UpdatedAt: row.UpdatedAt, Source: row.Source}, nil
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
				return apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
			}
			return err
		}
		if event.PaidDate != nil && customer.Last != nil && event.PaidDate.Before(*customer.Last) {
			return s.insertRecord(tx, p.TenantID, event, "IGNORED_OUT_OF_ORDER", s.graceDays, now, &result)
		}
		evaluation := "IGNORED_INCOMPLETE"
		if event.PaidDate != nil && paid.GreaterThanOrEqual(due) {
			deadline := event.DueDate.UTC().AddDate(0, 0, s.graceDays)
			if !event.PaidDate.After(deadline) {
				evaluation = "ONTIME"
			} else {
				evaluation = "LATE"
			}
		}
		if err := s.insertRecord(tx, p.TenantID, event, evaluation, s.graceDays, now, &result); err != nil {
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
		if evaluation == "ONTIME" && int(customer.OnTime) >= s.threshold {
			newLevel = stepLevel(customer.Level, -1)
			customer.OnTime = 0
		}
		if evaluation == "LATE" && int(customer.Late) >= s.threshold {
			newLevel = stepLevel(customer.Level, 1)
			customer.Late = 0
		}
		updates := map[string]any{"consecutive_ontime_count": customer.OnTime, "consecutive_late_count": customer.Late, "last_payment_eval_at": event.PaidDate}
		if newLevel != customer.Level {
			updates["credit_level"] = newLevel
			updates["credit_updated_at"] = now
			updates["credit_change_source"] = "RULE"
			if err := tx.Create(&CreditLog{TenantID: p.TenantID, CustomerID: event.CustomerID, FromLevel: customer.Level, ToLevel: newLevel, Source: "RULE", Reason: evaluation, EventID: event.EventID, OccurredAt: now}).Error; err != nil {
				return err
			}
		}
		return tx.Table("crm_customers").Where("tenant_id=? AND id=?", p.TenantID, event.CustomerID).Updates(updates).Error
	})
	return result, err
}

func (s *Service) insertRecord(tx *gorm.DB, tenant string, event PaymentEvent, evaluation string, grace int, now time.Time, result *PaymentRecord) error {
	r := creditPaymentRecord{TenantID: tenant, CustomerID: event.CustomerID, EventID: event.EventID, PaymentID: event.PaymentID, DueDate: &event.DueDate, PaidDate: event.PaidDate, DueAmount: event.DueAmount, PaidAmount: event.PaidAmount, Evaluation: evaluation, GraceDays: grace, EvaluatedAt: now, CreatedAt: now}
	if err := tx.Create(&r).Error; err != nil {
		return err
	}
	*result = toPaymentRecord(r)
	return nil
}
func toPaymentRecord(r creditPaymentRecord) PaymentRecord {
	return PaymentRecord{ID: r.ID, EventID: r.EventID, PaymentID: r.PaymentID, Evaluation: r.Evaluation, GraceDays: r.GraceDays, EvaluatedAt: r.EvaluatedAt}
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
	if !validLevel(in.TargetLevel) || len([]rune(in.Reason)) < 20 {
		return nil, apperror.New(400, "CRM_CREDIT_APPLICATION_INVALID", "target level or reason is invalid")
	}
	now := s.now()
	var out Application
	err := database.FromContext(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		var customer struct {
			ID                       uint64
			OwnerUserID, CreditLevel string
		}
		if err := tx.Table("crm_customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,owner_user_id,credit_level").Where("tenant_id=? AND id=? AND status='ACTIVE' AND merged_into_id IS NULL AND deleted_at IS NULL", p.TenantID, customerID).Take(&customer).Error; err != nil {
			return apperror.New(404, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
		}
		if customer.CreditLevel == in.TargetLevel {
			return apperror.New(409, "CRM_CREDIT_APPLICATION_SAME_LEVEL", "target level equals current level")
		}
		if customer.OwnerUserID != p.UserID && !hasRole(p, "sales_director") && !hasRole(p, "customer_admin") && !hasRole(p, "crm_super_admin") {
			return apperror.ErrForbidden
		}
		pending := customerID
		out = Application{TenantID: p.TenantID, CustomerID: customerID, ApplicantID: p.UserID, FromLevel: customer.CreditLevel, TargetLevel: in.TargetLevel, Reason: in.Reason, Status: "PENDING", CreatedAt: now, UpdatedAt: now, Version: 1, PendingCustomerID: &pending}
		if err := tx.Create(&out).Error; err != nil {
			return apperror.New(409, "CRM_CREDIT_APPLICATION_PENDING_EXISTS", "a pending credit application already exists")
		}
		return nil
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
				if err := tx.Create(&CreditLog{TenantID: p.TenantID, CustomerID: out.CustomerID, FromLevel: out.FromLevel, ToLevel: out.TargetLevel, Source: "MANUAL", Reason: in.Opinion, OccurredAt: now}).Error; err != nil {
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
		return tx.Save(&out).Error
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
		return tx.Save(&out).Error
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
func (s *Service) History(ctx context.Context, customerID uint64) ([]CreditLog, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrUnauthenticated
	}
	var items []CreditLog
	err := database.FromContext(ctx, s.db).Where("tenant_id=? AND customer_id=?", p.TenantID, customerID).Order("occurred_at DESC").Find(&items).Error
	return items, err
}
func (s *Service) Payments(ctx context.Context, customerID uint64) ([]PaymentRecord, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrUnauthenticated
	}
	var rows []creditPaymentRecord
	err := database.FromContext(ctx, s.db).Where("tenant_id=? AND customer_id=?", p.TenantID, customerID).Order("evaluated_at DESC").Find(&rows).Error
	items := make([]PaymentRecord, 0, len(rows))
	for _, r := range rows {
		items = append(items, toPaymentRecord(r))
	}
	return items, err
}
func hasRole(p auth.Principal, want string) bool {
	for _, role := range p.Roles {
		if role == want {
			return true
		}
	}
	return false
}
