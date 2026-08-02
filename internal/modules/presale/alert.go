package presale

import (
	"context"
	"errors"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
)

const (
	alertPending   = "PENDING"
	alertUnread    = "UNREAD"
	alertRead      = "READ"
	alertCancelled = "CANCELLED"
)

const (
	AlertRecipientUser   = "USER"
	AlertRecipientPerson = "PERSON"
)

var alertTypes = []AlertType{
	AlertApprovalNode1Overdue, AlertApprovalNode2Overdue, AlertAssignmentOverdue,
	AlertExecutionDueSoon, AlertExecutionOverdue,
}

type AlertService struct {
	db    *gorm.DB
	clock Clock
}

func NewAlertService(db *gorm.DB, clock Clock) *AlertService {
	if clock == nil {
		clock = SystemClock{}
	}
	return &AlertService{db: db, clock: clock}
}

func validAlertType(value AlertType) bool {
	for _, candidate := range alertTypes {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s *AlertService) ListRules(ctx context.Context, actor Actor) ([]AlertRuleView, error) {
	if !actor.Can("presale.alert.config") {
		return nil, ErrForbidden
	}
	var values []AlertRule
	if err := s.db.WithContext(ctx).Where("tenant_id=? AND deleted_at IS NULL", actor.TenantID).Order("type").Find(&values).Error; err != nil {
		return nil, err
	}
	result := make([]AlertRuleView, 0, len(values))
	for _, value := range values {
		result = append(result, alertRuleView(value))
	}
	return result, nil
}

func alertRuleView(value AlertRule) AlertRuleView {
	return AlertRuleView{Type: value.Type, ThresholdHours: value.ThresholdHours, Enabled: value.Enabled, ConfigVersion: value.ConfigVersion, UpdatedBy: value.UpdatedBy, UpdatedAt: value.UpdatedAt}
}

func (s *AlertService) UpdateRule(ctx context.Context, actor Actor, alertType AlertType, in UpdateAlertRuleInput) (AlertRuleView, error) {
	if !actor.Can("presale.alert.config") {
		return AlertRuleView{}, ErrForbidden
	}
	if !validAlertType(alertType) || in.Version == 0 || in.ThresholdHours > 8760 {
		return AlertRuleView{}, ErrInvalidInput
	}
	var output AlertRule
	err := database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		tx := database.FromContext(txCtx, s.db)
		var current AlertRule
		err := tx.Where("tenant_id=? AND type=? AND deleted_at IS NULL", actor.TenantID, alertType).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if in.Version != 1 {
				return ErrVersionConflict
			}
			current = AlertRule{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, Type: alertType, ThresholdHours: in.ThresholdHours, Enabled: in.Enabled, ConfigVersion: 1}
			if err = tx.Create(&current).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if current.Version != in.Version {
				return ErrVersionConflict
			}
			result := tx.Model(&AlertRule{}).Where("tenant_id=? AND id=? AND version=?", actor.TenantID, current.ID, in.Version).Updates(map[string]any{
				"threshold_hours": in.ThresholdHours, "enabled": in.Enabled, "config_version": gorm.Expr("config_version+1"),
				"updated_by": actor.UserID, "updated_at": s.clock.Now(), "version": gorm.Expr("version+1"),
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrVersionConflict
			}
			current.ThresholdHours, current.Enabled = in.ThresholdHours, in.Enabled
			current.ConfigVersion++
			current.Version++
			current.UpdatedBy, current.UpdatedAt = actor.UserID, s.clock.Now()
		}
		history := map[string]any{"tenant_id": actor.TenantID, "rule_id": current.ID, "type": current.Type, "threshold_hours": current.ThresholdHours, "enabled": current.Enabled, "config_version": current.ConfigVersion, "changed_by": actor.UserID, "request_id": actor.RequestID, "changed_at": s.clock.Now()}
		if err = tx.Table("crm_presale_alert_rule_versions").Create(history).Error; err != nil {
			return err
		}
		output = current
		return nil
	})
	return alertRuleView(output), err
}

func (s *AlertService) ListAlerts(ctx context.Context, actor Actor, unreadOnly bool, page, pageSize int) (AlertListPage, error) {
	if !actor.Can("presale.read") {
		return AlertListPage{}, ErrForbidden
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	db := s.db.WithContext(ctx).Table("crm_presale_alerts a").
		Joins("JOIN crm_presale_requests r ON r.tenant_id=a.tenant_id AND r.id=a.request_id AND r.deleted_at IS NULL").
		Where("a.tenant_id=? AND "+alertRecipientPredicate(actor)+" AND a.status IN ('UNREAD','READ') AND a.deleted_at IS NULL", alertRecipientArgs(actor.TenantID, actor)...)
	if unreadOnly {
		db = db.Where("a.status='UNREAD'")
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return AlertListPage{}, err
	}
	var values []AlertView
	err := db.Select("a.id,a.request_id,r.request_no,a.alert_type,a.rule_version,a.basis_at,a.due_at,a.status,a.recipient_kind,a.sent_at,a.read_at").
		Order("a.created_at DESC,a.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&values).Error
	return AlertListPage{Items: values, Page: page, PageSize: pageSize, Total: total}, err
}

func (s *AlertService) MarkRead(ctx context.Context, actor Actor, id uint64) error {
	if !actor.Can("presale.read") {
		return ErrForbidden
	}
	now := s.clock.Now()
	result := s.db.WithContext(ctx).Model(&Alert{}).
		Where("tenant_id=? AND id=? AND "+alertRecipientPredicate(actor)+" AND status='UNREAD' AND deleted_at IS NULL", append([]any{actor.TenantID, id}, alertRecipientArgsWithoutTenant(actor)...)...).
		Updates(map[string]any{"status": alertRead, "read_at": now, "updated_at": now, "updated_by": actor.UserID, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var status string
	err := s.db.WithContext(ctx).Model(&Alert{}).Select("status").Where("tenant_id=? AND id=? AND "+alertRecipientPredicate(actor)+" AND deleted_at IS NULL", append([]any{actor.TenantID, id}, alertRecipientArgsWithoutTenant(actor)...)...).Scan(&status).Error
	if err != nil || strings.TrimSpace(status) == "" {
		return ErrNotFound
	}
	if status == alertRead {
		return nil
	}
	return ErrInvalidTransition
}

// Alert recipients use explicit namespaces. Personal alert reads deliberately
// ignore SELF/ORG/ALL and only union the authenticated OIDC subject with the
// separately signed PMS person binding when that binding is non-empty.
func alertRecipientPredicate(actor Actor) string {
	if strings.TrimSpace(actor.PersonID) == "" {
		return "(recipient_kind=? AND recipient_id=?)"
	}
	return "((recipient_kind=? AND recipient_id=?) OR (recipient_kind=? AND recipient_id=?))"
}

func alertRecipientArgs(tenant string, actor Actor) []any {
	return append([]any{tenant}, alertRecipientArgsWithoutTenant(actor)...)
}

func alertRecipientArgsWithoutTenant(actor Actor) []any {
	values := []any{AlertRecipientUser, actor.UserID}
	if strings.TrimSpace(actor.PersonID) != "" {
		values = append(values, AlertRecipientPerson, actor.PersonID)
	}
	return values
}
