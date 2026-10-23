package handover

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// existingReceipt 在同一事务内做幂等短路。
func existingReceipt(tx *gorm.DB, in TransferInput) (Receipt, bool, error) {
	var row receiptRow
	err := tx.Where("tenant_id = ? AND idempotency_key = ?", in.TenantID, in.IdempotencyKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	return receiptFromRow(row), true, nil
}

// writeReceipt 写入回执并填充输出（REJECTED 同样落库，便于平台核对）。
func writeReceipt(tx *gorm.DB, in TransferInput, now time.Time, status string, applied uint64, reason, evidence string, outcome *Receipt) error {
	receiptID, err := NewID(now)
	if err != nil {
		return err
	}
	record := receiptRow{ID: receiptID, TenantID: in.TenantID, BatchID: in.BatchID, ItemID: in.ItemID,
		ResourceType: in.ResourceType, ResourceID: in.ResourceID, FromUserID: in.FromUserID, ToUserID: in.ToUserID,
		IdempotencyKey: in.IdempotencyKey, Status: status, AppliedVersion: applied,
		ReasonCode: optional(reason), EvidenceRef: optional(evidence), ReceivedAt: now.UTC(), CreatedAt: now.UTC()}
	if err := tx.Create(&record).Error; err != nil {
		if !isDuplicate(err) {
			return err
		}
		// 只有幂等键本身已存在时，重复才属于成功重放；
		// 其他唯一性冲突必须回滚。
		if lookupErr := tx.Where("tenant_id = ? AND idempotency_key = ?", in.TenantID, in.IdempotencyKey).Take(&record).Error; lookupErr != nil {
			return err
		}
	}
	*outcome = receiptFromRow(record)
	return nil
}

// transferTeamMembership 结束原成员身份并新增承接人成员身份（追加式，不覆盖历史）。
func (repository *GORMRepository) transferTeamMembership(ctx context.Context, in TransferInput, now time.Time) (Receipt, error) {
	opportunityID := strings.TrimPrefix(in.ResourceID, "opportunity-team:")
	var outcome Receipt
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if receipt, handled, err := existingReceipt(tx, in); err != nil || handled {
			outcome = receipt
			return err
		}
		var successorCount int64
		if err := tx.Table("crm_opportunity_members").
			Where("tenant_id = ? AND opportunity_id = ? AND user_id = ? AND deleted_at IS NULL", in.TenantID, opportunityID, in.ToUserID).
			Count(&successorCount).Error; err != nil {
			return err
		}
		// 承接人已是团队成员：不重复插入（唯一键约束），交回平台按 REJECTED 处理。
		if successorCount > 0 {
			return writeReceipt(tx, in, now, ReceiptRejected, 0, "CRM_TEAM_MEMBER_EXISTS", "", &outcome)
		}
		var prior struct {
			Role string `gorm:"column:role"`
		}
		if err := tx.Table("crm_opportunity_members").Select("role").
			Where("tenant_id = ? AND opportunity_id = ? AND user_id = ? AND is_active = ? AND deleted_at IS NULL AND version = ?", in.TenantID, opportunityID, in.FromUserID, true, in.ExpectedVersion).
			Take(&prior).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return writeReceipt(tx, in, now, ReceiptRejected, 0, versionConflictReason, "", &outcome)
			}
			return err
		}
		update := tx.Table("crm_opportunity_members").
			Where("tenant_id = ? AND opportunity_id = ? AND user_id = ? AND is_active = ? AND deleted_at IS NULL AND version = ?", in.TenantID, opportunityID, in.FromUserID, true, in.ExpectedVersion).
			Updates(map[string]any{"is_active": false, "ended_at": now.UTC(), "updated_at": now.UTC(), "updated_by": in.ToUserID, "version": gorm.Expr("version + 1")})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return writeReceipt(tx, in, now, ReceiptRejected, 0, versionConflictReason, "", &outcome)
		}
		if err := tx.Exec("INSERT INTO crm_opportunity_members (tenant_id, created_by, updated_by, created_at, updated_at, version, opportunity_id, user_id, role, is_active, ended_at) VALUES (?,?,?,?,?,1,?,?,?,1,NULL)",
			in.TenantID, "handover", "handover", now.UTC(), now.UTC(), opportunityID, in.ToUserID, prior.Role).Error; err != nil {
			return err
		}
		return writeReceipt(tx, in, now, ReceiptApplied, in.ExpectedVersion+1, "", "crm:opportunity-team:"+opportunityID, &outcome)
	})
	if err != nil {
		return Receipt{}, err
	}
	return outcome, nil
}

// transferPresaleAssignment 按既有领域语义追加式改派：结束当前承接并新增一条承接记录。
func (repository *GORMRepository) transferPresaleAssignment(ctx context.Context, in TransferInput, now time.Time) (Receipt, error) {
	requestID := strings.TrimPrefix(in.ResourceID, "presale:")
	var outcome Receipt
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if receipt, handled, err := existingReceipt(tx, in); err != nil || handled {
			outcome = receipt
			return err
		}
		// 团队负责人由技术总监在售前申请上选择，不属于可自动转给接任者的执行责任。
		// 存在当前 TEAM_LEAD 行时要求技术总监先重选，不能把负责人静默改成执行人。
		var teamLeadRows int64
		if err := tx.Table("crm_presale_assignments").
			Where("tenant_id = ? AND request_id = ? AND assignee_id = ? AND is_current = ? AND ended_at IS NULL AND deleted_at IS NULL AND assignment_kind = ?", in.TenantID, requestID, in.FromUserID, true, "TEAM_LEAD").
			Count(&teamLeadRows).Error; err != nil {
			return err
		}
		if teamLeadRows > 0 {
			return writeReceipt(tx, in, now, ReceiptRejected, 0, presaleTeamLeadReselectionReason, "", &outcome)
		}
		var prior struct {
			Role       string `gorm:"column:assignee_role"`
			Department string `gorm:"column:assignee_department_snapshot"`
			BatchNo    uint64 `gorm:"column:batch_no"`
		}
		if err := tx.Table("crm_presale_assignments").Select("assignee_role, assignee_department_snapshot, batch_no").
			Where("tenant_id = ? AND request_id = ? AND assignee_id = ? AND is_current = ? AND ended_at IS NULL AND deleted_at IS NULL AND assignment_kind = ? AND version = ?", in.TenantID, requestID, in.FromUserID, true, presaleExecutorAssignmentKind, in.ExpectedVersion).
			Take(&prior).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return writeReceipt(tx, in, now, ReceiptRejected, 0, versionConflictReason, "", &outcome)
			}
			return err
		}
		update := tx.Table("crm_presale_assignments").
			Where("tenant_id = ? AND request_id = ? AND assignee_id = ? AND is_current = ? AND ended_at IS NULL AND deleted_at IS NULL AND assignment_kind = ? AND version = ?", in.TenantID, requestID, in.FromUserID, true, presaleExecutorAssignmentKind, in.ExpectedVersion).
			Updates(map[string]any{"is_current": false, "ended_at": now.UTC(), "updated_at": now.UTC(), "updated_by": in.ToUserID, "version": gorm.Expr("version + 1")})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return writeReceipt(tx, in, now, ReceiptRejected, 0, versionConflictReason, "", &outcome)
		}
		if err := tx.Exec("INSERT INTO crm_presale_assignments (tenant_id, created_by, updated_by, created_at, updated_at, version, request_id, assignee_id, assignee_name_snapshot, assignee_department_snapshot, assignee_role, assignment_kind, assigned_by, assigned_at, ended_at, is_current, batch_no, change_reason) VALUES (?,?,?,?,?,1,?,?,?,?,?,?,?,?,NULL,1,?,?)",
			in.TenantID, "handover", "handover", now.UTC(), now.UTC(), requestID, in.ToUserID, in.ToUserID, prior.Department, prior.Role, presaleExecutorAssignmentKind, "handover", now.UTC(), prior.BatchNo+1, "人员离职交接改派").Error; err != nil {
			return err
		}
		return writeReceipt(tx, in, now, ReceiptApplied, in.ExpectedVersion+1, "", "crm:presale:"+requestID, &outcome)
	})
	if err != nil {
		return Receipt{}, err
	}
	return outcome, nil
}

// rejectEngineOwnedTransfer 对由外部引擎持有的责任项给出如实回执：本地不改数据、不冒充成功。
func (repository *GORMRepository) rejectEngineOwnedTransfer(ctx context.Context, in TransferInput, now time.Time, reason string) (Receipt, error) {
	var outcome Receipt
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if receipt, handled, err := existingReceipt(tx, in); err != nil || handled {
			outcome = receipt
			return err
		}
		return writeReceipt(tx, in, now, ReceiptRejected, 0, reason, "", &outcome)
	})
	if err != nil {
		return Receipt{}, err
	}
	return outcome, nil
}
