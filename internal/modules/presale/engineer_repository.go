package presale

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMEngineerDirectoryRepository struct {
	db  *gorm.DB
	ids IDGenerator
}

func NewGORMEngineerDirectoryRepository(db *gorm.DB, ids IDGenerator) *GORMEngineerDirectoryRepository {
	return &GORMEngineerDirectoryRepository{db: db, ids: ids}
}

func (r *GORMEngineerDirectoryRepository) ListAssignableEngineers(ctx context.Context, tenant string, query EngineerListQuery) (EngineerListPage, error) {
	db := r.db.WithContext(ctx).Model(&Engineer{}).Where("tenant_id=? AND deleted_at IS NULL AND valid_flag=1", tenant)
	if query.Keyword != "" {
		pattern := "%" + escapeLike(query.Keyword) + "%"
		db = db.Where("(person_id LIKE ? ESCAPE '\\\\' OR person_name LIKE ? ESCAPE '\\\\')", pattern, pattern)
	}
	if query.Department != "" {
		db = db.Where("department=?", query.Department)
	}
	if query.Role != "" {
		db = db.Where("role=?", query.Role)
	}
	if query.Skill != "" {
		db = db.Where("JSON_SEARCH(skill_tags_json, 'one', ?) IS NOT NULL", query.Skill)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return EngineerListPage{}, err
	}
	var values []Engineer
	if err := db.Order("person_name,person_id,id").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&values).Error; err != nil {
		return EngineerListPage{}, err
	}
	items := make([]EngineerView, 0, len(values))
	for _, value := range values {
		var skills []string
		if len(value.SkillTagsJSON) > 0 && string(value.SkillTagsJSON) != "null" {
			if err := json.Unmarshal(value.SkillTagsJSON, &skills); err != nil {
				return EngineerListPage{}, err
			}
		}
		items = append(items, EngineerView{PersonID: value.PersonID, PersonName: value.PersonName, Department: value.Department, Role: value.Role, SkillTags: skills, ValidFlag: value.ValidFlag, SourceUpdatedAt: value.SourceUpdatedAt, SyncedAt: value.SyncedAt})
	}
	var state EngineerSyncState
	err := r.db.WithContext(ctx).Where("tenant_id=?", tenant).Take(&state).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return EngineerListPage{}, err
	}
	result := EngineerListPage{Items: items, Page: query.Page, PageSize: query.PageSize, Total: total}
	if err == nil {
		result.LastAttemptAt, result.LastSuccessfulSyncAt, result.NextSyncAt = state.LastAttemptAt, state.LastSuccessfulAt, &state.NextSyncAt
	}
	return result, nil
}

func (r *GORMEngineerDirectoryRepository) EnqueueEngineerSync(ctx context.Context, tenant, actor, key, requestHash string, now time.Time) (*EngineerSyncJob, error) {
	var output *EngineerSyncJob
	err := database.WithTransaction(ctx, r.db, func(txCtx context.Context) error {
		tx := database.FromContext(txCtx, r.db)
		state := EngineerSyncState{BaseModel: BaseModel{TenantID: tenant, CreatedBy: actor, UpdatedBy: actor, Version: 1}, NextSyncAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}}, DoNothing: true}).Create(&state).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=?", tenant).Take(&state).Error; err != nil {
			return err
		}
		var replay EngineerSyncRequest
		err := tx.Where("tenant_id=? AND requested_by=? AND idempotency_key=?", tenant, actor, key).Take(&replay).Error
		if err == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			var replayJob EngineerSyncJob
			if err = tx.Where("tenant_id=? AND id=? AND job_no=?", tenant, replay.JobID, replay.JobNo).Take(&replayJob).Error; err != nil {
				return err
			}
			output = &replayJob
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var active EngineerSyncJob
		err = tx.Where("tenant_id=? AND status IN ?", tenant, []string{"PENDING", "PROCESSING", "RETRY_WAIT"}).Order("id").Take(&active).Error
		if err == nil {
			request := &EngineerSyncRequest{BaseModel: BaseModel{TenantID: tenant, CreatedBy: actor, UpdatedBy: actor, Version: 1}, RequestedBy: actor, IdempotencyKey: key, RequestHash: requestHash, JobID: active.ID, JobNo: active.JobNo}
			if err = tx.Create(request).Error; err != nil {
				return err
			}
			output = &active
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		job := &EngineerSyncJob{BaseModel: BaseModel{TenantID: tenant, CreatedBy: actor, UpdatedBy: actor, Version: 1}, JobNo: r.ids.NewID(), TriggerType: "MANUAL", RequestedBy: actor, IdempotencyKey: key, RequestHash: requestHash, Status: "PENDING"}
		if err = tx.Create(job).Error; err != nil {
			return err
		}
		request := &EngineerSyncRequest{BaseModel: BaseModel{TenantID: tenant, CreatedBy: actor, UpdatedBy: actor, Version: 1}, RequestedBy: actor, IdempotencyKey: key, RequestHash: requestHash, JobID: job.ID, JobNo: job.JobNo}
		if err = tx.Create(request).Error; err != nil {
			return err
		}
		output = job
		return nil
	})
	return output, err
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}
