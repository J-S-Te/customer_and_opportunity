package customer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const lockSourceOpportunitiesSQL = `SELECT id FROM crm_opportunities
WHERE tenant_id=? AND customer_id=? AND deleted_at IS NULL
ORDER BY id FOR UPDATE`

func (r *GORMRepository) WithMergeTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}

func (r *GORMRepository) LockCustomersForMerge(ctx context.Context, principal auth.Principal, sourceID, targetID uint64) (*Customer, *Customer, error) {
	ids := []uint64{sourceID, targetID}
	if sourceID > targetID {
		ids[0], ids[1] = targetID, sourceID
	}
	var models []Customer
	err := scopedCustomer(database.FromContext(ctx, r.db).Model(&Customer{}), principal).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("crm_customers.id IN ?", ids).
		Order("crm_customers.id ASC").Find(&models).Error
	if err != nil {
		return nil, nil, err
	}
	if len(models) != 2 {
		return nil, nil, ErrNotFound
	}
	var source, target *Customer
	for index := range models {
		switch models[index].ID {
		case sourceID:
			source = &models[index]
		case targetID:
			target = &models[index]
		}
	}
	if source == nil || target == nil {
		return nil, nil, ErrNotFound
	}
	return source, target, nil
}

func (r *GORMRepository) FindMergeIdempotency(ctx context.Context, tenantID, actorID, key string) (*MergeIdempotency, error) {
	var model MergeIdempotency
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND actor_id=? AND idempotency_key=?", tenantID, actorID, key).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

// LockMergeRelations 让终态合同写入及其他商机变更与客户合并串行；按主键排序加锁，
// 使所有合并事务采用确定的子记录锁顺序，降低死锁风险。
func (r *GORMRepository) LockMergeRelations(ctx context.Context, tenantID string, sourceID uint64) error {
	rows, err := database.FromContext(ctx, r.db).Raw(lockSourceOpportunitiesSQL, tenantID, sourceID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var opportunityID uint64
		if err = rows.Scan(&opportunityID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *GORMRepository) MergeBlockers(ctx context.Context, tenantID string, sourceID, targetID uint64) ([]MergeBlocker, error) {
	checks := []struct {
		table     string
		condition string
		code      string
		relation  string
		message   string
	}{
		{"crm_portal_identity_links", "deleted_at IS NULL", "PORTAL_IDENTITY_LINK", "portal_identity_links", "source customer has a Portal identity mapping in another subsystem; rebind must complete before merge"},
		{"crm_portal_compensation_tasks", "deleted_at IS NULL", "PORTAL_COMPENSATION_TASK", "portal_compensation_tasks", "source customer has an external provisioning compensation record that cannot be safely retargeted"},
	}
	blockers := make([]MergeBlocker, 0, len(checks))
	for _, check := range checks {
		var count int64
		err := database.FromContext(ctx, r.db).Table(check.table).
			Where("tenant_id=? AND customer_id=? AND "+check.condition, tenantID, sourceID).
			Count(&count).Error
		if err != nil {
			return nil, err
		}
		if count > 0 {
			blockers = append(blockers, MergeBlocker{Code: check.code, Relation: check.relation, Count: count, Message: check.message})
		}
	}
	var contractedOpportunities int64
	if err := database.FromContext(ctx, r.db).Table("crm_opportunities").
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL AND contract_ref IS NOT NULL AND TRIM(contract_ref)<>''", tenantID, sourceID).
		Count(&contractedOpportunities).Error; err != nil {
		return nil, err
	}
	if contractedOpportunities > 0 {
		blockers = append(blockers, MergeBlocker{Code: "SIGNED_CONTRACT_REBIND_REQUIRED", Relation: "opportunities.contract_ref", Count: contractedOpportunities, Message: "source customer has opportunities linked to contracts; contract ownership must be safely rebound before merge"})
	}
	var targetRegistrationContacts int64
	if err := database.FromContext(ctx, r.db).Table("crm_customer_contacts").
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL AND is_registration=TRUE", tenantID, targetID).
		Count(&targetRegistrationContacts).Error; err != nil {
		return nil, err
	}
	if targetRegistrationContacts != 1 {
		blockers = append(blockers, MergeBlocker{Code: "TARGET_REGISTRATION_CONTACT_INVALID", Relation: "customer_contacts", Count: targetRegistrationContacts, Message: "target customer must have exactly one active registration contact before merge"})
	}
	var sourcePendingInvites, targetPendingInvites int64
	if err := database.FromContext(ctx, r.db).Table("crm_portal_invites").
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL AND status='PENDING'", tenantID, sourceID).
		Count(&sourcePendingInvites).Error; err != nil {
		return nil, err
	}
	if sourcePendingInvites > 0 {
		if err := database.FromContext(ctx, r.db).Table("crm_portal_invites").
			Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL AND status='PENDING'", tenantID, targetID).
			Count(&targetPendingInvites).Error; err != nil {
			return nil, err
		}
	}
	if sourcePendingInvites > 0 && targetPendingInvites > 0 {
		blockers = append(blockers, MergeBlocker{Code: "PENDING_INVITE_CONFLICT", Relation: "portal_invites", Count: sourcePendingInvites + targetPendingInvites, Message: "source and target both have pending Portal invites; revoke one invite before merge"})
	}
	var sourceCurrentMappingRefs int64
	if err := database.FromContext(ctx, r.db).Table("crm_portal_invites AS i").
		Joins("JOIN crm_portal_identity_links AS l ON l.tenant_id=i.tenant_id AND l.customer_id=i.customer_id AND l.contact_id=i.contact_id AND l.deleted_at IS NULL").
		Where("i.tenant_id=? AND i.customer_id=? AND i.deleted_at IS NULL", tenantID, sourceID).
		Count(&sourceCurrentMappingRefs).Error; err != nil {
		return nil, err
	}
	if sourceCurrentMappingRefs > 0 {
		// 除直接映射阻断外继续保留跨表防御检查，避免未来调整映射状态过滤后让旧邀请变得可合并。
		alreadyBlocked := false
		for _, blocker := range blockers {
			if blocker.Code == "PORTAL_IDENTITY_LINK" {
				alreadyBlocked = true
				break
			}
		}
		if !alreadyBlocked {
			blockers = append(blockers, MergeBlocker{Code: "PORTAL_INVITE_MAPPING_REFERENCE", Relation: "portal_invites", Count: sourceCurrentMappingRefs, Message: "source customer invites reference a Portal identity mapping that requires coordinated rebind"})
		}
	}
	return blockers, nil
}

func (r *GORMRepository) MigrateMergeRelations(ctx context.Context, tenantID string, sourceID, targetID uint64, actorID string, now time.Time) (MergeMigrationCounts, error) {
	db := database.FromContext(ctx, r.db)
	counts := MergeMigrationCounts{}
	// 存续目标已经且只能有一个注册联系人；源客户联系人迁移后降为普通联系人，维持该不变量。
	contacts := db.Model(&Contact{}).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, sourceID).
		Updates(map[string]any{"customer_id": targetID, "is_registration": false, "updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if contacts.Error != nil {
		return counts, contacts.Error
	}
	counts.Contacts = contacts.RowsAffected

	stakeholders := db.Model(&Stakeholder{}).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, sourceID).
		Updates(map[string]any{"customer_id": targetID, "updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if stakeholders.Error != nil {
		return counts, stakeholders.Error
	}
	counts.Stakeholders = stakeholders.RowsAffected

	systems := db.Model(&InformationSystem{}).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, sourceID).
		Updates(map[string]any{"customer_id": targetID, "updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if systems.Error != nil {
		return counts, systems.Error
	}
	counts.Systems = systems.RowsAffected

	followups := db.Model(&Followup{}).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, sourceID).
		Updates(map[string]any{"customer_id": targetID, "updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if followups.Error != nil {
		return counts, followups.Error
	}
	counts.Followups = followups.RowsAffected

	opportunities := db.Table("crm_opportunities").
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, sourceID).
		Updates(map[string]any{"customer_id": targetID, "updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if opportunities.Error != nil {
		return counts, opportunities.Error
	}
	counts.Opportunities = opportunities.RowsAffected

	invites := db.Table("crm_portal_invites").
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, sourceID).
		Updates(map[string]any{"customer_id": targetID, "updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if invites.Error != nil {
		return counts, invites.Error
	}
	counts.PortalInvites = invites.RowsAffected
	return counts, nil
}

func (r *GORMRepository) MarkCustomersMerged(ctx context.Context, source, target *Customer, sourceVersion, targetVersion uint64, actorID string, now time.Time) error {
	db := database.FromContext(ctx, r.db)
	targetResult := db.Model(&Customer{}).
		Where("id=? AND tenant_id=? AND version=? AND status=? AND merged_into_id IS NULL AND deleted_at IS NULL", target.ID, target.TenantID, targetVersion, StatusActive).
		Updates(map[string]any{"updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if targetResult.Error != nil {
		return targetResult.Error
	}
	if targetResult.RowsAffected != 1 {
		return ErrVersionConflict
	}
	sourceResult := db.Model(&Customer{}).
		Where("id=? AND tenant_id=? AND version=? AND status=? AND merged_into_id IS NULL AND deleted_at IS NULL", source.ID, source.TenantID, sourceVersion, StatusActive).
		Updates(map[string]any{"status": StatusMerged, "merged_into_id": target.ID, "end_date": now, "updated_by": actorID, "updated_at": now, "version": gorm.Expr("version+1")})
	if sourceResult.Error != nil {
		return sourceResult.Error
	}
	if sourceResult.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (r *GORMRepository) CreateMergeLog(ctx context.Context, model *MergeLog) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) CreateMergeIdempotency(ctx context.Context, model *MergeIdempotency) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) CreateMergeOutbox(ctx context.Context, model *MergeOutboxEvent) error {
	// 事件进入供其他 CRM 集成消费的共享发件箱前，通过编解码防御性确认载荷仍是合法 JSON。
	var payload any
	if len(model.Payload) == 0 || json.Unmarshal(model.Payload, &payload) != nil {
		return errors.New("customer merge outbox payload is invalid")
	}
	return database.FromContext(ctx, r.db).Create(model).Error
}
