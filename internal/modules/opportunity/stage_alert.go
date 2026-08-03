package opportunity

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StageAlertPending   = "PENDING"
	StageAlertUnread    = "UNREAD"
	StageAlertRead      = "READ"
	StageAlertCancelled = "CANCELLED"
)

// StageAlertRule 保存推进阶段的当前阈值配置。每次接受配置写入都会提升 ConfigVersion，
// 该版本也是持久化告警身份的一部分，避免新旧规则混用。
type StageAlertRule struct {
	database.Model
	Stage          string `gorm:"size:32;not null"`
	ThresholdHours uint32 `gorm:"not null"`
	Enabled        bool   `gorm:"not null"`
	ConfigVersion  uint64 `gorm:"not null;default:1"`
}

func (StageAlertRule) TableName() string { return "crm_opportunity_stage_alert_rules" }

// StageAlert 同时作为站内通知投影：PENDING 行与发件箱事件原子提交，任务再把两者收敛到
// UNREAD/SENT；该状态不表示任何外部通知渠道已经投递。
type StageAlert struct {
	database.Model
	OpportunityID    uint64     `gorm:"not null;index"`
	Stage            string     `gorm:"size:32;not null"`
	ThresholdVersion uint64     `gorm:"not null"`
	BasisAt          time.Time  `gorm:"precision:3;not null"`
	DueAt            time.Time  `gorm:"precision:3;not null"`
	Status           string     `gorm:"size:16;not null"`
	RecipientID      string     `gorm:"size:64;not null;index"`
	SentAt           *time.Time `gorm:"precision:3"`
	ReadAt           *time.Time `gorm:"precision:3"`
}

func (StageAlert) TableName() string { return "crm_opportunity_stage_alerts" }

type UpdateStageAlertRuleRequest struct {
	ThresholdHours uint32 `json:"threshold_hours" binding:"required"`
	Enabled        bool   `json:"enabled"`
	Version        uint64 `json:"version" binding:"required"`
}

type StageAlertRuleResponse struct {
	Stage          string    `json:"stage"`
	ThresholdHours uint32    `json:"threshold_hours"`
	Enabled        bool      `json:"enabled"`
	ConfigVersion  uint64    `json:"config_version"`
	Version        uint64    `json:"version"`
	UpdatedBy      string    `json:"updated_by"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type StageAlertResponse struct {
	ID               uint64     `json:"id"`
	OpportunityID    uint64     `json:"opportunity_id"`
	OpportunityNo    string     `json:"opportunity_no"`
	Stage            string     `json:"stage"`
	ThresholdVersion uint64     `json:"threshold_version"`
	BasisAt          time.Time  `json:"basis_at"`
	DueAt            time.Time  `json:"due_at"`
	Status           string     `json:"status"`
	SentAt           *time.Time `json:"sent_at,omitempty"`
	ReadAt           *time.Time `json:"read_at,omitempty"`
}

type StageAlertPage = pagination.Page[StageAlertResponse]

type StageAlertService struct {
	db  *gorm.DB
	now func() time.Time
}

func NewStageAlertService(db *gorm.DB) *StageAlertService {
	return &StageAlertService{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func timedStages() []string {
	return []string{StageInitial, StageRequirement, StageSolution, StageQuotation, StageBid}
}

func isTimedStage(stage string) bool {
	for _, candidate := range timedStages() {
		if stage == candidate {
			return true
		}
	}
	return false
}

func (s *StageAlertService) ListRules(ctx context.Context) ([]StageAlertRuleResponse, error) {
	principal, err := requireAlertPrincipal(ctx, "opportunity.alert.config")
	if err != nil {
		return nil, err
	}
	var rules []StageAlertRule
	if err = s.db.WithContext(ctx).Where("tenant_id=? AND deleted_at IS NULL", principal.TenantID).Order("stage").Find(&rules).Error; err != nil {
		return nil, err
	}
	result := make([]StageAlertRuleResponse, 0, len(rules))
	for _, rule := range rules {
		result = append(result, stageAlertRuleResponse(rule))
	}
	return result, nil
}

func (s *StageAlertService) UpdateRule(ctx context.Context, stage string, input UpdateStageAlertRuleRequest) (StageAlertRuleResponse, error) {
	principal, err := requireAlertPrincipal(ctx, "opportunity.alert.config")
	if err != nil {
		return StageAlertRuleResponse{}, err
	}
	stage = strings.TrimSpace(stage)
	if !isTimedStage(stage) || input.ThresholdHours == 0 || input.ThresholdHours > 8760 || input.Version == 0 {
		return StageAlertRuleResponse{}, ErrInvalidAlertRule
	}
	now := s.now().UTC()
	var output StageAlertRule
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		tx := database.FromContext(txCtx, s.db)
		var current StageAlertRule
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND stage=? AND deleted_at IS NULL", principal.TenantID, stage).Take(&current).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			if input.Version != 1 {
				return ErrVersionConflict
			}
			current = StageAlertRule{
				Model: database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, CreatedAt: now, UpdatedAt: now, Version: 1},
				Stage: stage, ThresholdHours: input.ThresholdHours, Enabled: input.Enabled, ConfigVersion: 1,
			}
			if createErr := tx.Create(&current).Error; createErr != nil {
				return createErr
			}
		case findErr != nil:
			return findErr
		default:
			if current.Version != input.Version {
				return ErrVersionConflict
			}
			result := tx.Model(&StageAlertRule{}).
				Where("tenant_id=? AND id=? AND version=? AND deleted_at IS NULL", principal.TenantID, current.ID, input.Version).
				Updates(map[string]any{
					"threshold_hours": input.ThresholdHours, "enabled": input.Enabled,
					"config_version": gorm.Expr("config_version+1"), "version": gorm.Expr("version+1"),
					"updated_by": principal.UserID, "updated_at": now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrVersionConflict
			}
			current.ThresholdHours, current.Enabled = input.ThresholdHours, input.Enabled
			current.ConfigVersion++
			current.Version++
			current.UpdatedBy, current.UpdatedAt = principal.UserID, now
		}
		history := map[string]any{
			"tenant_id": principal.TenantID, "rule_id": current.ID, "stage": current.Stage,
			"threshold_hours": current.ThresholdHours, "enabled": current.Enabled,
			"config_version": current.ConfigVersion, "changed_by": principal.UserID,
			"request_id": request.ID(ctx), "changed_at": now,
		}
		if historyErr := tx.Table("crm_opportunity_stage_alert_rule_versions").Create(history).Error; historyErr != nil {
			return historyErr
		}
		output = current
		return nil
	})
	if err != nil {
		return StageAlertRuleResponse{}, err
	}
	return stageAlertRuleResponse(output), nil
}

func stageAlertRuleResponse(rule StageAlertRule) StageAlertRuleResponse {
	return StageAlertRuleResponse{
		Stage: rule.Stage, ThresholdHours: rule.ThresholdHours, Enabled: rule.Enabled,
		ConfigVersion: rule.ConfigVersion, Version: rule.Version,
		UpdatedBy: rule.UpdatedBy, UpdatedAt: rule.UpdatedAt,
	}
}

func (s *StageAlertService) ListMine(ctx context.Context, unreadOnly bool, page, pageSize int) (StageAlertPage, error) {
	principal, err := requireAlertPrincipal(ctx, "opportunity.read")
	if err != nil {
		return StageAlertPage{}, err
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	query := s.db.WithContext(ctx).Table("crm_opportunity_stage_alerts a").
		Joins("JOIN crm_opportunities o ON o.tenant_id=a.tenant_id AND o.id=a.opportunity_id").
		Where("a.tenant_id=? AND a.recipient_id=? AND a.status IN ('UNREAD','READ') AND a.deleted_at IS NULL", principal.TenantID, principal.UserID)
	query = scopeStageAlertOpportunities(query, principal)
	if unreadOnly {
		query = query.Where("a.status='UNREAD'")
	}
	var total int64
	if err = query.Count(&total).Error; err != nil {
		return StageAlertPage{}, err
	}
	var items []StageAlertResponse
	err = query.Select("a.id,a.opportunity_id,o.opportunity_no,a.stage,a.threshold_version,a.basis_at,a.due_at,a.status,a.sent_at,a.read_at").
		Order("a.created_at DESC,a.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&items).Error
	return StageAlertPage{Items: items, Page: page, PageSize: pageSize, Total: total}, err
}

func scopeStageAlertOpportunities(db *gorm.DB, principal auth.Principal) *gorm.DB {
	switch principal.ScopeMode {
	case auth.ScopeAll:
		return db
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return db.Where("1=0")
		}
		return db.Where("o.owner_org_id IN ?", principal.OrganizationIDs)
	default:
		return db.Where("o.owner_user_id=?", principal.UserID)
	}
}

func (s *StageAlertService) MarkRead(ctx context.Context, alertID uint64) error {
	principal, err := requireAlertPrincipal(ctx, "opportunity.read")
	if err != nil {
		return err
	}
	now := s.now().UTC()
	result := s.db.WithContext(ctx).Model(&StageAlert{}).
		Where("tenant_id=? AND id=? AND recipient_id=? AND status='UNREAD' AND deleted_at IS NULL", principal.TenantID, alertID, principal.UserID).
		Updates(map[string]any{"status": StageAlertRead, "read_at": now, "updated_at": now, "updated_by": principal.UserID, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var status string
	err = s.db.WithContext(ctx).Model(&StageAlert{}).Select("status").
		Where("tenant_id=? AND id=? AND recipient_id=? AND deleted_at IS NULL", principal.TenantID, alertID, principal.UserID).
		Scan(&status).Error
	if err != nil || status == "" {
		return ErrAlertNotFound
	}
	if status == StageAlertRead {
		return nil
	}
	return ErrAlertNotReadable
}

func requireAlertPrincipal(ctx context.Context, permission string) (auth.Principal, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok || principal.UserID == "" || principal.TenantID == "" {
		return auth.Principal{}, apperror.ErrUnauthenticated
	}
	if !principal.HasPermission(permission) {
		return auth.Principal{}, apperror.ErrForbidden
	}
	return principal, nil
}

// cancelActiveStageAlerts 在商机状态变更事务内调用，用于关闭定时扫描刚生成告警、
// 商机随后立即离开计时阶段的竞态窗口。
func cancelActiveStageAlerts(ctx context.Context, db *gorm.DB, tenantID string, opportunityID uint64, actorID string, now time.Time) error {
	tx := database.FromContext(ctx, db)
	var ids []uint64
	if err := tx.Model(&StageAlert{}).
		Where("tenant_id=? AND opportunity_id=? AND status IN ('PENDING','UNREAD')", tenantID, opportunityID).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Model(&StageAlert{}).Where("id IN ? AND status IN ('PENDING','UNREAD')", ids).
		Updates(map[string]any{"status": StageAlertCancelled, "updated_at": now.UTC(), "updated_by": actorID, "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	aggregateIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		aggregateIDs = append(aggregateIDs, strconv.FormatUint(id, 10))
	}
	return tx.Model(&OutboxEvent{}).
		Where("event_type=? AND aggregate_type=? AND aggregate_id IN ? AND status='PENDING'", "OPPORTUNITY_STAGE_ALERT_SITE_MESSAGE", "opportunity_stage_alert", aggregateIDs).
		Update("status", "CANCELLED").Error
}
