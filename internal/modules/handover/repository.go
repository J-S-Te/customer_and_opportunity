package handover

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// GORMRepository 是交接协议的持久化实现（CRM）。
// 关键不变量：回执存在 ⇒ 责任变更已在同一事务内生效。
type GORMRepository struct {
	db          *gorm.DB
	orgResolver OwnerOrgResolver
}

// OwnerOrgResolver 解析新负责人的主组织。
// 装配入口接入平台 owner-directory；未接入时，
// 为兼容单元测试夹具，交接保留原组织。
type OwnerOrgResolver interface {
	PrimaryOrganization(ctx context.Context, tenantID, userID string) (string, error)
}

func (repository *GORMRepository) UseOwnerOrgResolver(resolver OwnerOrgResolver) *GORMRepository {
	repository.orgResolver = resolver
	return repository
}

var _ Repository = (*GORMRepository)(nil)

func NewGORMRepository(db *gorm.DB) (*GORMRepository, error) {
	if db == nil {
		return nil, errors.New("handover repository requires a database")
	}
	return &GORMRepository{db: db}, nil
}

type freezeRow struct {
	ID         string     `gorm:"column:id;size:26;primaryKey"`
	TenantID   string     `gorm:"column:tenant_id;size:64;uniqueIndex:uk_crm_handover_freeze_batch_user,priority:1"`
	BatchID    string     `gorm:"column:batch_id;size:26;uniqueIndex:uk_crm_handover_freeze_batch_user,priority:2"`
	RequestID  string     `gorm:"column:request_id"`
	UserID     string     `gorm:"column:user_id;size:64;uniqueIndex:uk_crm_handover_freeze_batch_user,priority:3"`
	Status     string     `gorm:"column:status"`
	FrozenAt   time.Time  `gorm:"column:frozen_at"`
	ReleasedAt *time.Time `gorm:"column:released_at"`
	Outcome    *string    `gorm:"column:outcome"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}

func (freezeRow) TableName() string { return "crm_handover_freeze" }

type receiptRow struct {
	ID             string    `gorm:"column:id;size:26;primaryKey"`
	TenantID       string    `gorm:"column:tenant_id;size:64;uniqueIndex:uk_crm_handover_receipt_key,priority:1"`
	BatchID        string    `gorm:"column:batch_id"`
	ItemID         string    `gorm:"column:item_id"`
	ResourceType   string    `gorm:"column:resource_type"`
	ResourceID     string    `gorm:"column:resource_id"`
	FromUserID     string    `gorm:"column:from_user_id"`
	ToUserID       string    `gorm:"column:to_user_id"`
	IdempotencyKey string    `gorm:"column:idempotency_key;size:160;uniqueIndex:uk_crm_handover_receipt_key,priority:2"`
	Status         string    `gorm:"column:status"`
	AppliedVersion uint64    `gorm:"column:applied_version"`
	ReasonCode     *string   `gorm:"column:reason_code"`
	EvidenceRef    *string   `gorm:"column:evidence_ref"`
	ReceivedAt     time.Time `gorm:"column:received_at"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (receiptRow) TableName() string { return "crm_handover_receipt" }

// 有效客户与未完成商机的判定：客户 ACTIVE、商机 FOLLOWING（其余为终态）。
const (
	customerActiveStatus  = "ACTIVE"
	opportunityOpenStatus = "FOLLOWING"
	versionConflictReason = "CRM_VERSION_CONFLICT"
	// TS-011 团队负责人不是执行责任，不能由离职交接自动改派给其他人。
	presaleTeamLeadReselectionReason = "CRM_PRESALE_TEAM_LEAD_REQUIRES_RESELECTION"
	presaleExecutorAssignmentKind    = "EXECUTOR"
)

type responsibilityRow struct {
	ID          uint64    `gorm:"column:id"`
	Name        string    `gorm:"column:name"`
	OwnerUserID string    `gorm:"column:owner_user_id"`
	Version     uint64    `gorm:"column:version"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

// CreateFreeze 幂等创建冻结。
func (repository *GORMRepository) CreateFreeze(ctx context.Context, freeze Freeze) (Freeze, bool, error) {
	record := freezeRow{ID: freeze.ID, TenantID: freeze.TenantID, BatchID: freeze.BatchID, RequestID: freeze.RequestID,
		UserID: freeze.UserID, Status: freeze.Status, FrozenAt: freeze.FrozenAt.UTC(),
		CreatedAt: freeze.FrozenAt.UTC(), UpdatedAt: freeze.FrozenAt.UTC()}
	if err := repository.db.WithContext(ctx).Create(&record).Error; err != nil {
		if !isDuplicate(err) {
			return Freeze{}, false, err
		}
		existing, found, lookupErr := repository.ActiveFreeze(ctx, freeze.TenantID, freeze.UserID)
		if lookupErr != nil || !found {
			return Freeze{}, false, fmt.Errorf("handover freeze lookup failed: %v", lookupErr)
		}
		return existing, false, nil
	}
	return freezeFromRow(record), true, nil
}

// ReleaseFreeze 解除冻结；重复解除幂等。
func (repository *GORMRepository) ReleaseFreeze(ctx context.Context, tenantID, batchID, outcome string, now time.Time) error {
	update := repository.db.WithContext(ctx).Model(&freezeRow{}).
		Where("tenant_id = ? AND batch_id = ? AND status = ?", tenantID, batchID, FreezeFrozen).
		Updates(map[string]any{"status": FreezeReleased, "released_at": now.UTC(), "outcome": outcome, "updated_at": now.UTC()})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 0 {
		var count int64
		if err := repository.db.WithContext(ctx).Model(&freezeRow{}).Where("tenant_id = ? AND batch_id = ?", tenantID, batchID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// ActiveFreeze 判断用户当前是否被交接冻结（写路径据此拒绝新责任）。
func (repository *GORMRepository) ActiveFreeze(ctx context.Context, tenantID, userID string) (Freeze, bool, error) {
	var record freezeRow
	err := repository.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND status = ?", tenantID, userID, FreezeFrozen).
		Order("frozen_at DESC").Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Freeze{}, false, nil
	}
	if err != nil {
		return Freeze{}, false, err
	}
	return freezeFromRow(record), true, nil
}

// ListPendingApprovalInstances 返回待审的售前审批实例候选（有界、按租户）。
// 审批人由审批引擎计算、本地不存储，因此这里只给候选，由服务层向引擎确认。
func (repository *GORMRepository) ListPendingApprovalInstances(ctx context.Context, tenantID string, limit int) ([]ApprovalInstance, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []struct {
		InstanceID       uint64    `gorm:"column:instance_id"`
		RequestID        uint64    `gorm:"column:request_id"`
		EngineInstanceID string    `gorm:"column:engine_instance_id"`
		CurrentNode      uint8     `gorm:"column:current_node"`
		Title            string    `gorm:"column:title"`
		Version          uint64    `gorm:"column:version"`
		UpdatedAt        time.Time `gorm:"column:updated_at"`
	}
	if err := repository.db.WithContext(ctx).Table("crm_presale_approval_instances AS i").
		Select("i.id AS instance_id, i.request_id, i.engine_instance_id, i.current_node, r.title, i.version, i.updated_at").
		Joins("JOIN crm_presale_requests AS r ON r.id = i.request_id AND r.tenant_id = i.tenant_id AND r.deleted_at IS NULL").
		Where("i.tenant_id = ? AND i.deleted_at IS NULL AND i.finished_at IS NULL AND i.status NOT IN ?", tenantID, []string{"APPROVED", "REJECTED", "CANCELLED"}).
		Order("i.id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ApprovalInstance, 0, len(rows))
	for _, row := range rows {
		out = append(out, ApprovalInstance{InstanceID: row.InstanceID, RequestID: row.RequestID, EngineInstanceID: row.EngineInstanceID,
			CurrentNode: row.CurrentNode, Title: row.Title, Version: row.Version, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

// ActiveFreezeUser 是写路径窄端口适配：只回答"该用户当前是否被冻结"。
func (repository *GORMRepository) ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error) {
	_, found, err := repository.ActiveFreeze(ctx, tenantID, userID)
	return found, err
}

// ListResponsibilities 返回有效客户 + 未完成商机（本人负责），按资源标识排序分页。
func (repository *GORMRepository) ListResponsibilities(ctx context.Context, tenantID, userID, cursor string, limit int) ([]Responsibility, error) {
	if limit <= 0 {
		limit = 100
	}
	items := []Responsibility{}
	var customers []responsibilityRow
	customerQuery := repository.db.WithContext(ctx).Table("crm_customers").
		Select("id, name, owner_user_id, version, updated_at").
		Where("tenant_id = ? AND owner_user_id = ? AND status = ? AND deleted_at IS NULL", tenantID, userID, customerActiveStatus)
	if cursor != "" {
		customerQuery = customerQuery.Where("CONCAT('customer:', id) > ?", cursor)
	}
	if err := customerQuery.Order("id ASC").Limit(limit).Scan(&customers).Error; err != nil {
		return nil, err
	}
	for _, row := range customers {
		items = append(items, Responsibility{
			ResourceType: ResourceCustomer, ResourceID: fmt.Sprintf("customer:%d", row.ID), ResourceName: row.Name,
			OwnerUserID: row.OwnerUserID, Category: CategoryCustomerOwner, Version: row.Version, UpdatedAt: row.UpdatedAt,
		})
	}
	var opportunities []responsibilityRow
	opportunityQuery := repository.db.WithContext(ctx).Table("crm_opportunities").
		Select("id, name, owner_user_id, version, updated_at").
		Where("tenant_id = ? AND owner_user_id = ? AND opp_status = ? AND deleted_at IS NULL", tenantID, userID, opportunityOpenStatus)
	if cursor != "" {
		opportunityQuery = opportunityQuery.Where("CONCAT('opportunity:', id) > ?", cursor)
	}
	if err := opportunityQuery.Order("id ASC").Limit(limit).Scan(&opportunities).Error; err != nil {
		return nil, err
	}
	for _, row := range opportunities {
		items = append(items, Responsibility{
			ResourceType: ResourceOpportunity, ResourceID: fmt.Sprintf("opportunity:%d", row.ID), ResourceName: row.Name,
			OwnerUserID: row.OwnerUserID, Category: CategoryOpportunityOwner, Version: row.Version, UpdatedAt: row.UpdatedAt,
		})
	}
	// 活动团队成员：成员仍有效且商机未结束。
	var members []responsibilityRow
	memberQuery := repository.db.WithContext(ctx).Table("crm_opportunity_members AS m").
		Select("m.opportunity_id AS id, o.name AS name, m.user_id AS owner_user_id, m.version AS version, m.updated_at AS updated_at").
		Joins("JOIN crm_opportunities AS o ON o.id = m.opportunity_id AND o.tenant_id = m.tenant_id AND o.deleted_at IS NULL AND o.opp_status = ?", opportunityOpenStatus).
		Where("m.tenant_id = ? AND m.user_id = ? AND m.is_active = ? AND m.deleted_at IS NULL", tenantID, userID, true)
	if cursor != "" {
		memberQuery = memberQuery.Where("CONCAT('opportunity-team:', m.opportunity_id) > ?", cursor)
	}
	if err := memberQuery.Order("m.opportunity_id ASC").Limit(limit).Scan(&members).Error; err != nil {
		return nil, err
	}
	for _, row := range members {
		items = append(items, Responsibility{
			ResourceType: ResourceOpportunityTeam, ResourceID: fmt.Sprintf("opportunity-team:%d", row.ID), ResourceName: row.Name,
			OwnerUserID: row.OwnerUserID, Category: CategoryOpportunityTeam, Version: row.Version, UpdatedAt: row.UpdatedAt,
		})
	}
	// 未完成售前执行任务：当前承接人且请求未结束。
	var presales []responsibilityRow
	presaleQuery := repository.db.WithContext(ctx).Table("crm_presale_assignments AS a").
		Select("a.request_id AS id, r.title AS name, a.assignee_id AS owner_user_id, a.version AS version, a.updated_at AS updated_at").
		Joins("JOIN crm_presale_requests AS r ON r.id = a.request_id AND r.tenant_id = a.tenant_id AND r.deleted_at IS NULL").
		Where("a.tenant_id = ? AND a.assignee_id = ? AND a.is_current = ? AND a.ended_at IS NULL AND a.deleted_at IS NULL AND a.assignment_kind = ? AND r.status NOT IN ?",
			tenantID, userID, true, presaleExecutorAssignmentKind, []string{presaleCompletedStatus, presaleRejectedStatus, presaleCancelledStatus})
	if cursor != "" {
		presaleQuery = presaleQuery.Where("CONCAT('presale:', a.request_id) > ?", cursor)
	}
	if err := presaleQuery.Order("a.request_id ASC").Limit(limit).Scan(&presales).Error; err != nil {
		return nil, err
	}
	for _, row := range presales {
		items = append(items, Responsibility{
			ResourceType: ResourcePresaleTask, ResourceID: fmt.Sprintf("presale:%d", row.ID), ResourceName: row.Name,
			OwnerUserID: row.OwnerUserID, Category: CategoryPresaleTask, Version: row.Version, UpdatedAt: row.UpdatedAt,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ResourceID < items[j].ResourceID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// TransferOwnership 在本地事务内变更责任归属并写入幂等回执。
func (repository *GORMRepository) TransferOwnership(ctx context.Context, in TransferInput, now time.Time) (Receipt, error) {
	switch in.ResourceType {
	case ResourcePresaleApproval:
		// 审批待办由审批引擎持有：本地不得改库冒充已改派，如实返回 REJECTED 供运维在引擎侧处理。
		return repository.rejectEngineOwnedTransfer(ctx, in, now, "PRESALE_APPROVAL_ENGINE_OWNED")
	case ResourceOpportunityTeam:
		return repository.transferTeamMembership(ctx, in, now)
	case ResourcePresaleTask:
		return repository.transferPresaleAssignment(ctx, in, now)
	}
	table, ok := responsibilityTable(in.ResourceType)
	if !ok {
		return Receipt{}, fmt.Errorf("unsupported resource type %s: %w", in.ResourceType, ErrValidation)
	}
	resourceID := strings.TrimPrefix(strings.TrimPrefix(in.ResourceID, "customer:"), "opportunity:")
	var outcome Receipt
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing receiptRow
		err := tx.Where("tenant_id = ? AND idempotency_key = ?", in.TenantID, in.IdempotencyKey).Take(&existing).Error
		if err == nil {
			outcome = receiptFromRow(existing)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		fields := map[string]any{"owner_user_id": in.ToUserID, "updated_by": in.ToUserID, "updated_at": now.UTC(), "version": gorm.Expr("version + 1")}
		if repository.orgResolver != nil {
			orgID, resolveErr := repository.orgResolver.PrimaryOrganization(ctx, in.TenantID, in.ToUserID)
			if resolveErr != nil {
				return resolveErr
			}
			if strings.TrimSpace(orgID) == "" {
				return fmt.Errorf("owner organization is unavailable: %w", ErrValidation)
			}
			fields["owner_org_id"] = strings.TrimSpace(orgID)
		}
		update := tx.Table(table).
			Where("tenant_id = ? AND id = ? AND owner_user_id = ? AND version = ? AND deleted_at IS NULL", in.TenantID, resourceID, in.FromUserID, in.ExpectedVersion).
			Updates(fields)
		if update.Error != nil {
			return update.Error
		}
		status, reason, applied := ReceiptApplied, "", in.ExpectedVersion+1
		if update.RowsAffected != 1 {
			// 版本或当前负责人不符：写入 REJECTED 回执，绝不改数据。
			status, reason, applied = ReceiptRejected, versionConflictReason, 0
		}
		evidence := ""
		if status == ReceiptApplied {
			evidence = "crm:" + in.ResourceType + ":" + resourceID
		}
		receiptID, idErr := NewID(now)
		if idErr != nil {
			return idErr
		}
		record := receiptRow{ID: receiptID, TenantID: in.TenantID, BatchID: in.BatchID, ItemID: in.ItemID,
			ResourceType: in.ResourceType, ResourceID: in.ResourceID, FromUserID: in.FromUserID, ToUserID: in.ToUserID,
			IdempotencyKey: in.IdempotencyKey, Status: status, AppliedVersion: applied,
			ReasonCode: optional(reason), EvidenceRef: optional(evidence), ReceivedAt: now.UTC(), CreatedAt: now.UTC()}
		if err := tx.Create(&record).Error; err != nil {
			if isDuplicate(err) {
				// 只有相同幂等键的并发重放才是
				// 成功的空操作。主键冲突属于真实错误，
				// 必须回滚责任更新，禁止携带
				// 空回执报告成功。
				var existing receiptRow
				if lookupErr := tx.Where("tenant_id = ? AND idempotency_key = ?", in.TenantID, in.IdempotencyKey).Take(&existing).Error; lookupErr == nil {
					outcome = receiptFromRow(existing)
					return nil
				}
			}
			return err
		}
		outcome = receiptFromRow(record)
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	return outcome, nil
}

// FindReceipt 按幂等键查询既有回执。
func (repository *GORMRepository) FindReceipt(ctx context.Context, tenantID, idempotencyKey string) (Receipt, bool, error) {
	var record receiptRow
	err := repository.db.WithContext(ctx).Where("tenant_id = ? AND idempotency_key = ?", tenantID, idempotencyKey).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	return receiptFromRow(record), true, nil
}

func responsibilityTable(resourceType string) (string, bool) {
	switch resourceType {
	case ResourceCustomer:
		return "crm_customers", true
	case ResourceOpportunity:
		return "crm_opportunities", true
	default:
		return "", false
	}
}

func freezeFromRow(row freezeRow) Freeze {
	outcome := ""
	if row.Outcome != nil {
		outcome = *row.Outcome
	}
	return Freeze{ID: row.ID, TenantID: row.TenantID, BatchID: row.BatchID, RequestID: row.RequestID,
		UserID: row.UserID, Status: row.Status, FrozenAt: row.FrozenAt, ReleasedAt: row.ReleasedAt, Outcome: outcome}
}

func receiptFromRow(row receiptRow) Receipt {
	reason, evidence := "", ""
	if row.ReasonCode != nil {
		reason = *row.ReasonCode
	}
	if row.EvidenceRef != nil {
		evidence = *row.EvidenceRef
	}
	return Receipt{ItemID: row.ItemID, IdempotencyKey: row.IdempotencyKey, Status: row.Status,
		AppliedVersion: row.AppliedVersion, ReasonCode: reason, EvidenceRef: evidence, ReceivedAt: row.ReceivedAt}
}

func optional(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	return &trimmed
}

func isDuplicate(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate")
}

// 售前请求的终态：这些请求的承接任务不再计入交接责任。
const (
	presaleCompletedStatus = "COMPLETED"
	presaleRejectedStatus  = "REJECTED"
	presaleCancelledStatus = "CANCELLED"
)
