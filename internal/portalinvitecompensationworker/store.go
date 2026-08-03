package portalinvitecompensationworker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"gorm.io/gorm"
)

type store struct{ db *gorm.DB }

func newStore(db *gorm.DB) *store { return &store{db: db} }

const claimTasksSQL = `SELECT * FROM crm_portal_compensation_tasks
WHERE deleted_at IS NULL AND
 ((status IN ('PENDING','RETRY_WAIT') AND (next_retry_at IS NULL OR next_retry_at<=?))
  OR (status='PROCESSING' AND locked_until<?))
ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`

// 领取事务只在建立有限租约时持行锁；过期 PROCESSING 可被其他副本回收，进程崩溃不会永久挂起补偿。
func (s *store) claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]portalinvite.CompensationTask, error) {
	var claimed []portalinvite.CompensationTask
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tasks []portalinvite.CompensationTask
		if err := tx.Raw(claimTasksSQL, now, now, limit).Scan(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(tasks))
		for i := range tasks {
			ids = append(ids, tasks[i].ID)
		}
		lockedUntil := now.Add(lease)
		result := tx.Model(&portalinvite.CompensationTask{}).Where("id IN ?", ids).Updates(map[string]any{
			"status": portalinvite.CompensationProcessing, "locked_by": workerID,
			"locked_until": lockedUntil, "last_attempt_at": now, "updated_by": workerID,
			"updated_at": now, "version": gorm.Expr("version+1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errLeaseLost
		}
		for i := range tasks {
			tasks[i].Status, tasks[i].LockedBy, tasks[i].LockedUntil = portalinvite.CompensationProcessing, workerID, &lockedUntil
			tasks[i].LastAttemptAt, tasks[i].Version = &now, tasks[i].Version+1
		}
		claimed = tasks
		return nil
	})
	return claimed, err
}

func (s *store) completeRole(ctx context.Context, task portalinvite.CompensationTask, workerID string, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 角色补偿完成时必须在同一事务持久化下一段映射补偿；复用父操作号生成稳定子键，使事务重放保持幂等。
		nextTaskNo := task.TaskNo + "M"
		if strings.HasSuffix(task.TaskNo, "R") {
			nextTaskNo = strings.TrimSuffix(task.TaskNo, "R") + "M"
		}
		nextTask := portalinvite.CompensationTask{
			TaskNo: nextTaskNo, TaskType: portalinvite.CompensationMapping,
			CustomerID: task.CustomerID, ContactID: task.ContactID,
			PlatformUserID: task.PlatformUserID, AccountNo: task.AccountNo,
			Status: portalinvite.CompensationPending,
		}
		nextTask.TenantID, nextTask.CreatedBy, nextTask.UpdatedBy = task.TenantID, workerID, workerID
		nextTask.CreatedAt, nextTask.UpdatedAt, nextTask.Version = now, now, 1
		if err := tx.Where("tenant_id=? AND task_no=?", task.TenantID, nextTask.TaskNo).FirstOrCreate(&nextTask).Error; err != nil {
			return err
		}
		return newStore(tx).complete(ctx, task, workerID, now, nil)
	})
}

func (s *store) completeMapping(ctx context.Context, task portalinvite.CompensationTask, workerID string, mapping portalinvite.PortalMapping, now time.Time) error {
	if mapping.PortalAccountID == "" {
		return errLeaseLost
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var link portalinvite.IdentityLink
		err := tx.Where("tenant_id=? AND customer_id=? AND contact_id=? AND deleted_at IS NULL", task.TenantID, task.CustomerID, task.ContactID).Take(&link).Error
		if err == nil {
			if link.PlatformUserID != task.PlatformUserID || (link.PortalAccountID != "" && link.PortalAccountID != mapping.PortalAccountID) {
				return errLeaseLost
			}
			// DISABLED 是管理员终态。映射补偿可能因网络结果不确定而在远端迟到成功，但不得借此复活 CRM 访问投影。
			if link.Status == "DISABLED" {
				return errLeaseLost
			}
			result := tx.Model(&link).Where("version=?", link.Version).Updates(map[string]any{
				"portal_account_id": mapping.PortalAccountID, "status": "PENDING", "updated_by": workerID,
				"updated_at": now, "version": gorm.Expr("version+1"),
			})
			if result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return errLeaseLost
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else {
			link = portalinvite.IdentityLink{
				CustomerID: task.CustomerID, ContactID: task.ContactID,
				PlatformUserID: task.PlatformUserID, PortalAccountID: mapping.PortalAccountID,
				Status: "PENDING", ProvisionedAt: now,
			}
			link.TenantID, link.CreatedBy, link.UpdatedBy = task.TenantID, workerID, workerID
			link.CreatedAt, link.UpdatedAt, link.Version = now, now, 1
			if err = tx.Create(&link).Error; err != nil {
				return err
			}
		}
		return newStore(tx).complete(ctx, task, workerID, now, nil)
	})
}

func (s *store) complete(ctx context.Context, task portalinvite.CompensationTask, workerID string, now time.Time, extra map[string]any) error {
	updates := map[string]any{
		"status": portalinvite.CompensationSucceeded, "completed_at": now,
		"locked_by": "", "locked_until": nil, "next_retry_at": nil,
		"last_error_code": "", "last_error_summary": "", "updated_by": workerID,
		"updated_at": now, "version": gorm.Expr("version+1"),
	}
	for key, value := range extra {
		updates[key] = value
	}
	result := s.db.WithContext(ctx).Model(&portalinvite.CompensationTask{}).
		Where("id=? AND version=? AND status=? AND locked_by=?", task.ID, task.Version, portalinvite.CompensationProcessing, workerID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (s *store) failed(ctx context.Context, task portalinvite.CompensationTask, workerID string, now time.Time, value failure) error {
	attempt := task.Attempts + 1
	status, next := failurePlan(now, attempt)
	result := s.db.WithContext(ctx).Model(&portalinvite.CompensationTask{}).
		Where("id=? AND version=? AND status=? AND locked_by=?", task.ID, task.Version, portalinvite.CompensationProcessing, workerID).
		Updates(map[string]any{
			"status": status, "attempts": attempt, "next_retry_at": next,
			"locked_by": "", "locked_until": nil, "last_error_code": value.code,
			"last_error_summary": sanitizeSummary(value.summary), "updated_by": workerID,
			"updated_at": now, "version": gorm.Expr("version+1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func sanitizeSummary(value string) string {
	value = stringsTrimSpaceAndControls(value)
	runes := []rune(value)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return value
}

func stringsTrimSpaceAndControls(value string) string {
	result := make([]rune, 0, len(value))
	for _, char := range []rune(value) {
		if char == '\r' || char == '\n' || char == '\t' {
			char = ' '
		}
		result = append(result, char)
	}
	start, end := 0, len(result)
	for start < end && result[start] == ' ' {
		start++
	}
	for end > start && result[end-1] == ' ' {
		end--
	}
	return string(result[start:end])
}
