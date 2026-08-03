package portalprojectworker

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

var errLeaseLost = errors.New("Portal project sync lease was lost")

type store struct{ db *gorm.DB }

func newStore(db *gorm.DB) *store { return &store{db: db} }

// 从已启用的 Portal 身份映射发现同步边界；INSERT IGNORE 只补缺失游标，不会重置已有客户的增量位置。
func (s *store) seedCustomers(ctx context.Context, tenantID string, now time.Time) error {
	return s.db.WithContext(ctx).Exec(`INSERT IGNORE INTO portal_project_sync_states
(tenant_id,customer_id,sync_cursor,next_run_at,last_error_summary,locked_by,created_at,updated_at,version)
SELECT tenant_id,customer_id,'',?,'','',?,?,1
FROM portal_identity_links
WHERE tenant_id=? AND status='ACTIVE' AND deleted_at IS NULL
GROUP BY tenant_id,customer_id`, now, now, now, tenantID).Error
}

// 领取只用短事务建立租约，持锁期间不发 HTTP；SKIP LOCKED 让多副本直接通过数据库分片，无需进程内协调。
func (s *store) claim(ctx context.Context, tenantID, workerID string, now time.Time, lease time.Duration) (*syncState, error) {
	var claimed *syncState
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state syncState
		err := tx.Raw(`SELECT s.* FROM portal_project_sync_states s
WHERE s.tenant_id=? AND s.next_run_at<=? AND (s.locked_until IS NULL OR s.locked_until<?)
AND EXISTS (SELECT 1 FROM portal_identity_links i WHERE i.tenant_id=s.tenant_id AND i.customer_id=s.customer_id AND i.status='ACTIVE' AND i.deleted_at IS NULL)
ORDER BY s.next_run_at,s.customer_id LIMIT 1 FOR UPDATE SKIP LOCKED`, tenantID, now, now).Scan(&state).Error
		if err != nil || state.ID == 0 {
			return err
		}
		lockedUntil := now.Add(lease)
		result := tx.Model(&syncState{}).Where("id=? AND version=?", state.ID, state.Version).
			Updates(map[string]any{"locked_by": workerID, "locked_until": lockedUntil, "last_attempt_at": now, "updated_at": now, "version": gorm.Expr("version+1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		state.LockedBy, state.LockedUntil, state.LastAttemptAt, state.Version = workerID, &lockedUntil, &now, state.Version+1
		claimed = &state
		return nil
	})
	return claimed, err
}

func (s *store) applyPage(ctx context.Context, state *syncState, workerID string, page sourcePage, now time.Time, done bool, syncInterval, leaseDuration time.Duration) (int, error) {
	// 项目快照、子项替换和游标推进必须原子提交；否则游标前移会让失败页永久漏同步。
	updated := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current syncState
		if err := tx.Raw("SELECT * FROM portal_project_sync_states WHERE id=? FOR UPDATE", state.ID).Scan(&current).Error; err != nil {
			return err
		}
		if current.ID == 0 || current.LockedBy != workerID || current.Version != state.Version || current.LockedUntil == nil || !current.LockedUntil.After(now) {
			return errLeaseLost
		}
		repo := project.NewGORMRepository(tx)
		txCtx := database.WithHandle(ctx, tx)
		for i := range page.Bundles {
			bundle := toProjectBundle(state.TenantID, state.CustomerID, page.Bundles[i], now, workerID)
			changed, err := repo.UpsertBundle(txCtx, &bundle)
			if err != nil {
				return err
			}
			if changed {
				updated++
			}
		}
		updates := map[string]any{"sync_cursor": page.NextCursor, "last_error_summary": "", "updated_at": now, "version": gorm.Expr("version+1")}
		if done {
			updates["locked_by"], updates["locked_until"] = "", nil
			updates["last_success_at"], updates["next_run_at"] = now, now.Add(syncInterval)
		} else {
			updates["locked_until"] = now.Add(leaseDuration)
		}
		result := tx.Model(&syncState{}).Where("id=? AND version=? AND locked_by=?", state.ID, state.Version, workerID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		state.Cursor, state.Version = page.NextCursor, state.Version+1
		if done {
			state.LockedBy, state.LockedUntil = "", nil
		} else {
			lockedUntil := now.Add(leaseDuration)
			state.LockedUntil = &lockedUntil
		}
		return nil
	})
	return updated, err
}

func (s *store) failed(ctx context.Context, state *syncState, workerID string, now time.Time, retryInterval time.Duration, summary string) error {
	// 写回条件包含版本和所有者，上一任 Worker 的迟到失败不能覆盖新领取者的进度。
	result := s.db.WithContext(ctx).Model(&syncState{}).
		Where("id=? AND version=? AND locked_by=?", state.ID, state.Version, workerID).
		Updates(map[string]any{"locked_by": "", "locked_until": nil, "next_run_at": now.Add(retryInterval), "last_error_summary": sanitize(summary), "updated_at": now, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func toProjectBundle(tenantID string, customerID uint64, source sourceBundle, now time.Time, actor string) project.Bundle {
	result := project.Bundle{Snapshot: project.Snapshot{
		ProjectID: source.ProjectID, CustomerID: customerID, ProjectName: source.ProjectName,
		ContractNo: source.ContractNo, Status: source.Status, ProgressPct: source.ProgressPct,
		CurrentStage: source.CurrentStage, ExpectedEndDate: source.ExpectedEndDate, Delayed: source.Delayed,
		ManagerName: source.ManagerName, ManagerContactMasked: source.ManagerContactMasked,
		ManagerPortalAccountID: source.ManagerPortalAccountID,
		SourceUpdatedAt:        source.SourceUpdatedAt, SyncedAt: now, RawVersion: source.RawVersion,
	}}
	result.Snapshot.TenantID, result.Snapshot.CreatedBy, result.Snapshot.UpdatedBy = tenantID, actor, actor
	for _, item := range source.Milestones {
		result.Milestones = append(result.Milestones, project.Milestone{StageCode: item.StageCode, StageName: item.StageName, Status: item.Status, PlannedAt: item.PlannedAt, CompletedAt: item.CompletedAt, SortNo: item.SortNo})
	}
	for _, item := range source.Activities {
		result.Activities = append(result.Activities, project.Activity{SourceActivityID: item.SourceActivityID, Type: item.Type, Content: item.Content, OccurredAt: item.OccurredAt})
	}
	for _, item := range source.Team {
		result.Team = append(result.Team, project.TeamMember{PersonRef: item.PersonRef, Name: item.Name, Role: item.Role, ContactMasked: item.ContactMasked})
	}
	return result
}

func sanitize(value string) string {
	value = stringsNewlines(value)
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}

func stringsNewlines(value string) string {
	result := make([]rune, 0, len(value))
	for _, char := range []rune(value) {
		if char == '\n' || char == '\r' || char == '\t' {
			char = ' '
		}
		result = append(result, char)
	}
	return string(result)
}
