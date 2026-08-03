package project

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }

func (r *GORMRepository) session(ctx context.Context) *gorm.DB {
	return database.FromContext(ctx, r.db)
}

func (r *GORMRepository) List(ctx context.Context, scope Scope, query ListQuery) (pagination.Page[Snapshot], error) {
	page := pagination.Page[Snapshot]{Items: []Snapshot{}, Page: query.Page, PageSize: query.PageSize}
	db := r.session(ctx).Where("tenant_id = ? AND customer_id = ? AND deleted_at IS NULL", scope.TenantID, scope.CustomerID)
	if query.Status != "" {
		db = db.Where("status = ?", query.Status)
	}
	if err := db.Model(&Snapshot{}).Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Order("source_updated_at DESC, id DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&page.Items).Error
	return page, err
}

type projectSyncState struct {
	LastSuccessAt *time.Time `gorm:"column:last_success_at"`
}

func (r *GORMRepository) LastSuccessfulSync(ctx context.Context, scope Scope) (*time.Time, error) {
	var state projectSyncState
	err := successfulSyncQuery(r.session(ctx), scope).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return state.LastSuccessAt, nil
}

func successfulSyncQuery(db *gorm.DB, scope Scope) *gorm.DB {
	return db.Table("portal_project_sync_states").
		Select("last_success_at").
		Where("tenant_id = ? AND customer_id = ?", scope.TenantID, scope.CustomerID)
}

func (r *GORMRepository) Find(ctx context.Context, scope Scope, projectID string) (*Detail, error) {
	var result Detail
	db := r.session(ctx)
	err := visibleSnapshotQuery(db, scope, projectID).Take(&result.Snapshot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	filter := "tenant_id = ? AND customer_id = ? AND project_id = ?"
	if err = db.Where(filter, scope.TenantID, scope.CustomerID, projectID).Order("sort_no,id").Find(&result.Milestones).Error; err != nil {
		return nil, err
	}
	if err = db.Where(filter, scope.TenantID, scope.CustomerID, projectID).Order("id").Find(&result.Team).Error; err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *GORMRepository) AssertVisible(ctx context.Context, scope Scope, projectID string) error {
	var value Snapshot
	err := visibleSnapshotQuery(r.session(ctx).Select("id"), scope, projectID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func visibleSnapshotQuery(db *gorm.DB, scope Scope, projectID string) *gorm.DB {
	return db.Where("tenant_id = ? AND customer_id = ? AND project_id = ?", scope.TenantID, scope.CustomerID, projectID)
}
func (r *GORMRepository) FindStatusForEvaluation(ctx context.Context, scope Scope, projectID string) (string, error) {
	var value Snapshot
	err := evaluationStatusQuery(r.session(ctx), scope, projectID).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrNotFound
	}
	return value.Status, err
}

func evaluationStatusQuery(db *gorm.DB, scope Scope, projectID string) *gorm.DB {
	return db.Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id", "status").
		Where("tenant_id = ? AND customer_id = ? AND project_id = ? AND deleted_at IS NULL", scope.TenantID, scope.CustomerID, projectID)
}
func (r *GORMRepository) ListActivities(ctx context.Context, scope Scope, projectID string, pageNo, pageSize int) (pagination.Page[Activity], error) {
	page := pagination.Page[Activity]{Items: []Activity{}, Page: pageNo, PageSize: pageSize}
	db := r.session(ctx).Where("tenant_id = ? AND customer_id = ? AND project_id = ?", scope.TenantID, scope.CustomerID, projectID)
	if err := db.Model(&Activity{}).Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Order("occurred_at DESC,id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&page.Items).Error
	return page, err
}
func (r *GORMRepository) UpsertBundle(ctx context.Context, bundle *Bundle) (bool, error) {
	if bundle == nil {
		return false, errors.New("project bundle is required")
	}
	changed := false
	operation := func(txCtx context.Context) error {
		tx := database.FromContext(txCtx, r.db)
		var existing Snapshot
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND customer_id = ? AND project_id = ?", bundle.Snapshot.TenantID, bundle.Snapshot.CustomerID, bundle.Snapshot.ProjectID).Take(&existing).Error
		if err == nil && !isNewerSource(bundle.Snapshot.SourceUpdatedAt, existing.SourceUpdatedAt) {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Create(&bundle.Snapshot).Error; err != nil {
				return err
			}
		} else {
			updates := snapshotUpdates(&bundle.Snapshot)
			result := tx.Model(&Snapshot{}).
				Where("id = ? AND tenant_id = ? AND customer_id = ? AND project_id = ? AND source_updated_at < ?", existing.ID, bundle.Snapshot.TenantID, bundle.Snapshot.CustomerID, bundle.Snapshot.ProjectID, bundle.Snapshot.SourceUpdatedAt).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return nil
			}
			bundle.Snapshot.ID = existing.ID
		}
		normalizeChildren(bundle)
		for _, model := range []any{&Milestone{}, &Activity{}, &TeamMember{}} {
			if err = tx.Where("tenant_id = ? AND customer_id = ? AND project_id = ?", bundle.Snapshot.TenantID, bundle.Snapshot.CustomerID, bundle.Snapshot.ProjectID).Delete(model).Error; err != nil {
				return err
			}
		}
		if len(bundle.Milestones) > 0 {
			if err = tx.Create(&bundle.Milestones).Error; err != nil {
				return err
			}
		}
		if len(bundle.Activities) > 0 {
			if err = tx.Create(&bundle.Activities).Error; err != nil {
				return err
			}
		}
		if len(bundle.Team) > 0 {
			if err = tx.Create(&bundle.Team).Error; err != nil {
				return err
			}
		}
		changed = true
		return nil
	}
	var err error
	if database.FromContext(ctx, nil) != nil {
		err = operation(ctx)
	} else {
		err = database.WithTransaction(ctx, r.db, operation)
	}
	return changed, err
}

func isNewerSource(candidate, current time.Time) bool { return candidate.After(current) }

func snapshotUpdates(value *Snapshot) map[string]any {
	return map[string]any{
		"project_name": value.ProjectName, "contract_no": value.ContractNo,
		"status": value.Status, "progress_pct": value.ProgressPct,
		"current_stage": value.CurrentStage, "expected_end_date": value.ExpectedEndDate,
		"delayed": value.Delayed, "manager_name_snapshot": value.ManagerName,
		"manager_contact_masked":    value.ManagerContactMasked,
		"manager_portal_account_id": value.ManagerPortalAccountID,
		"source_updated_at":         value.SourceUpdatedAt, "synced_at": value.SyncedAt,
		"raw_version": value.RawVersion, "updated_by": value.UpdatedBy,
		"updated_at": time.Now().UTC(), "version": gorm.Expr("version + 1"),
	}
}

// normalizeChildren 以已验证的父级范围为准，上游响应不能把子记录移动到其他租户、客户或项目。
func normalizeChildren(bundle *Bundle) {
	for i := range bundle.Milestones {
		bundle.Milestones[i].TenantID = bundle.Snapshot.TenantID
		bundle.Milestones[i].CustomerID = bundle.Snapshot.CustomerID
		bundle.Milestones[i].ProjectID = bundle.Snapshot.ProjectID
	}
	for i := range bundle.Activities {
		bundle.Activities[i].TenantID = bundle.Snapshot.TenantID
		bundle.Activities[i].CustomerID = bundle.Snapshot.CustomerID
		bundle.Activities[i].ProjectID = bundle.Snapshot.ProjectID
	}
	for i := range bundle.Team {
		bundle.Team[i].TenantID = bundle.Snapshot.TenantID
		bundle.Team[i].CustomerID = bundle.Snapshot.CustomerID
		bundle.Team[i].ProjectID = bundle.Snapshot.ProjectID
	}
}
