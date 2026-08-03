package filing

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	Create(context.Context, *Filing) error
	FindCreateAction(context.Context, Actor, string) (*Filing, error)
	ListOwned(context.Context, Actor, int, int) ([]Filing, int64, error)
	FindOwned(context.Context, Actor, string) (*Filing, error)
	FindOwnedForUpdate(context.Context, Actor, string) (*Filing, error)
	FindInternalForUpdate(context.Context, string, uint64, string) (*Filing, error)
	UpdateFiling(context.Context, *Filing, uint64, map[string]any) error
	FindSection(context.Context, string, uint64, string) (*Section, error)
	ListSections(context.Context, string, uint64) ([]Section, error)
	CreateSection(context.Context, *Section) error
	UpdateSection(context.Context, *Section, uint64, []byte, string, string, time.Time) error
	FindMatrix(context.Context, string, uint64, string) (*MatrixSelection, error)
	ListMatrices(context.Context, string, uint64) ([]MatrixSelection, error)
	CreateMatrix(context.Context, *MatrixSelection) error
	UpdateMatrix(context.Context, *MatrixSelection, uint64, string, string, bool, string, time.Time) error
	NextSubmissionSequence(context.Context, string, uint64) (uint64, error)
	CreateSubmission(context.Context, *SubmissionSnapshot) error
	CreateSubmissionOutbox(context.Context, *SubmissionOutbox) error
	CancelWaitingSubmissionOutbox(context.Context, string, uint64, time.Time) error
	CreateMaterial(context.Context, *Material) error
	FindMaterial(context.Context, string, uint64, string) (*Material, error)
	FindMaterialByCreate(context.Context, string, string, string) (*Material, error)
	FindMaterialByPublicIDForUpdate(context.Context, string, uint64, string) (*Material, error)
	FindMaterialForScanUpdate(context.Context, string, string) (*Material, error)
	ListMaterials(context.Context, string, uint64) ([]Material, error)
	UpdateMaterial(context.Context, *Material, uint64, map[string]any) error
	LatestSubmission(context.Context, string, uint64) (*SubmissionSnapshot, error)
	FindAction(context.Context, string, uint64, string, string, string) (*Action, error)
	CreateAction(context.Context, *Action) error
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository       { return &GORMRepository{db: db} }
func (r *GORMRepository) tx(ctx context.Context) *gorm.DB { return database.FromContext(ctx, r.db) }
func (r *GORMRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}
func (r *GORMRepository) Create(ctx context.Context, value *Filing) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindCreateAction(ctx context.Context, actor Actor, key string) (*Filing, error) {
	var value Filing
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND account_id=? AND create_idempotency_key=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, actor.AccountID, key).Take(&value).Error
	return filingResult(&value, err)
}
func (r *GORMRepository) ListOwned(ctx context.Context, actor Actor, page, pageSize int) ([]Filing, int64, error) {
	values := []Filing{}
	db := r.tx(ctx).Model(&Filing{}).Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := db.Order("updated_at DESC,id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&values).Error
	return values, total, err
}
func (r *GORMRepository) FindOwned(ctx context.Context, actor Actor, publicID string) (*Filing, error) {
	var value Filing
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND public_id=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, publicID).Take(&value).Error
	return filingResult(&value, err)
}
func (r *GORMRepository) FindOwnedForUpdate(ctx context.Context, actor Actor, publicID string) (*Filing, error) {
	// 行锁仍同时限定租户、客户和账号，不能为加锁便利而退化为仅主键查询。
	var value Filing
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND customer_id=? AND public_id=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, publicID).Take(&value).Error
	return filingResult(&value, err)
}
func (r *GORMRepository) FindInternalForUpdate(ctx context.Context, tenant string, customerID uint64, publicID string) (*Filing, error) {
	var value Filing
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND customer_id=? AND public_id=? AND deleted_at IS NULL", tenant, customerID, publicID).Take(&value).Error
	return filingResult(&value, err)
}
func (r *GORMRepository) UpdateFiling(ctx context.Context, value *Filing, version uint64, fields map[string]any) error {
	fields["version"] = gorm.Expr("version+1")
	result := r.tx(ctx).Model(&Filing{}).Where("id=? AND tenant_id=? AND version=? AND deleted_at IS NULL", value.ID, value.TenantID, version).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	value.Version = version + 1
	return nil
}
func (r *GORMRepository) FindSection(ctx context.Context, tenant string, filingID uint64, code string) (*Section, error) {
	var value Section
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=? AND section_code=?", tenant, filingID, code).Take(&value).Error
	return sectionResult(&value, err)
}
func (r *GORMRepository) ListSections(ctx context.Context, tenant string, filingID uint64) ([]Section, error) {
	values := []Section{}
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=?", tenant, filingID).Find(&values).Error
	return values, err
}
func (r *GORMRepository) CreateSection(ctx context.Context, value *Section) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) UpdateSection(ctx context.Context, value *Section, version uint64, data []byte, status, actor string, at time.Time) error {
	result := r.tx(ctx).Model(&Section{}).Where("id=? AND tenant_id=? AND filing_id=? AND version=?", value.ID, value.TenantID, value.FilingID, version).Updates(map[string]any{"data_cipher": data, "validation_status": status, "updated_by": actor, "updated_at": at, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) FindMatrix(ctx context.Context, tenant string, filingID uint64, code string) (*MatrixSelection, error) {
	var value MatrixSelection
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=? AND matrix_code=?", tenant, filingID, code).Take(&value).Error
	return matrixResult(&value, err)
}
func (r *GORMRepository) ListMatrices(ctx context.Context, tenant string, filingID uint64) ([]MatrixSelection, error) {
	values := []MatrixSelection{}
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=?", tenant, filingID).Find(&values).Error
	return values, err
}
func (r *GORMRepository) CreateMatrix(ctx context.Context, value *MatrixSelection) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) UpdateMatrix(ctx context.Context, value *MatrixSelection, version uint64, row, column string, selected bool, actor string, at time.Time) error {
	result := r.tx(ctx).Model(&MatrixSelection{}).Where("id=? AND tenant_id=? AND filing_id=? AND version=?", value.ID, value.TenantID, value.FilingID, version).Updates(map[string]any{"row_code": row, "column_code": column, "selected": selected, "updated_by": actor, "updated_at": at, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) NextSubmissionSequence(ctx context.Context, tenant string, filingID uint64) (uint64, error) {
	// 调用方已锁定备案头记录，序号只用于同一聚合内不可变快照排序。
	var max uint64
	err := r.tx(ctx).Model(&SubmissionSnapshot{}).Where("tenant_id=? AND filing_id=?", tenant, filingID).Select("COALESCE(MAX(sequence),0)").Scan(&max).Error
	return max + 1, err
}
func (r *GORMRepository) CreateSubmission(ctx context.Context, value *SubmissionSnapshot) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateSubmissionOutbox(ctx context.Context, value *SubmissionOutbox) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CancelWaitingSubmissionOutbox(ctx context.Context, tenant string, filingID uint64, at time.Time) error {
	// 解锁只取消尚未签订外部契约的等待项；已领取或已投递任务不能被追溯取消。
	return r.tx(ctx).Model(&SubmissionOutbox{}).
		Where("tenant_id=? AND filing_id=? AND status='WAITING_CONTRACT'", tenant, filingID).
		Updates(map[string]any{"status": "CANCELED", "next_retry_at": nil, "locked_by": "", "locked_until": nil, "last_error_summary": "canceled by authorized filing unlock", "sent_at": at.UTC()}).Error
}
func (r *GORMRepository) CreateMaterial(ctx context.Context, value *Material) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindMaterial(ctx context.Context, tenant string, filingID uint64, code string) (*Material, error) {
	var value Material
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=? AND material_code=?", tenant, filingID, code).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMaterialNotFound
	}
	return &value, err
}
func (r *GORMRepository) FindMaterialByCreate(ctx context.Context, tenant, actor, keyHash string) (*Material, error) {
	var value Material
	err := r.tx(ctx).Where("tenant_id=? AND create_actor_id=? AND create_key_hash=?", tenant, actor, keyHash).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMaterialNotFound
	}
	return &value, err
}
func (r *GORMRepository) FindMaterialByPublicIDForUpdate(ctx context.Context, tenant string, filingID uint64, publicID string) (*Material, error) {
	var value Material
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND filing_id=? AND public_id=?", tenant, filingID, publicID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMaterialNotFound
	}
	return &value, err
}
func (r *GORMRepository) FindMaterialForScanUpdate(ctx context.Context, tenant, publicID string) (*Material, error) {
	var value Material
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND public_id=?", tenant, publicID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMaterialNotFound
	}
	return &value, err
}
func (r *GORMRepository) ListMaterials(ctx context.Context, tenant string, filingID uint64) ([]Material, error) {
	values := []Material{}
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=?", tenant, filingID).Order("material_code,id").Find(&values).Error
	return values, err
}
func (r *GORMRepository) UpdateMaterial(ctx context.Context, value *Material, version uint64, fields map[string]any) error {
	fields["version"] = gorm.Expr("version+1")
	result := r.tx(ctx).Model(&Material{}).Where("id=? AND tenant_id=? AND filing_id=? AND version=?", value.ID, value.TenantID, value.FilingID, version).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	value.Version = version + 1
	return nil
}
func (r *GORMRepository) LatestSubmission(ctx context.Context, tenant string, filingID uint64) (*SubmissionSnapshot, error) {
	var value SubmissionSnapshot
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=?", tenant, filingID).Order("sequence DESC").Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) FindAction(ctx context.Context, tenant string, filingID uint64, actor, action, key string) (*Action, error) {
	var value Action
	err := r.tx(ctx).Where("tenant_id=? AND filing_id=? AND actor_id=? AND action=? AND idempotency_key=?", tenant, filingID, actor, action, key).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) CreateAction(ctx context.Context, value *Action) error {
	return r.tx(ctx).Create(value).Error
}

func filingResult(value *Filing, err error) (*Filing, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return value, err
}
func sectionResult(value *Section, err error) (*Section, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return value, err
}
func matrixResult(value *MatrixSelection, err error) (*MatrixSelection, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return value, err
}
