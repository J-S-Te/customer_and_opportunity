package opportunity

import (
	"context"
	"errors"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CommercialRecordRepository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	CreateRecord(context.Context, *CommercialRecord) error
	FindRecord(context.Context, string, uint64, string, bool) (*CommercialRecord, error)
	ListRecords(context.Context, string, uint64, int, int) ([]CommercialRecord, int64, error)
	SaveRecord(context.Context, *CommercialRecord, uint64) error
	ResolveAttachments(context.Context, string, uint64, []string, bool) ([]Attachment, error)
	ReplaceRecordAttachments(context.Context, *CommercialRecord, []Attachment) error
	ListRecordAttachments(context.Context, string, uint64, []uint64) (map[uint64][]Attachment, error)
}

type GORMCommercialRecordRepository struct{ db *gorm.DB }

// NewGORMCommercialRecordRepository 用途：接入现有 CRM 的共享数据库与事务句柄。
// 参数：db 为 CRM 数据库连接；不创建独立连接或读取运行配置。
// 返回值：人工资料仓储。
func NewGORMCommercialRecordRepository(db *gorm.DB) *GORMCommercialRecordRepository {
	return &GORMCommercialRecordRepository{db: db}
}

// WithTransaction 用途：使资料、关联、父锁、幂等及审计使用同一事务。
// 参数：ctx 为业务上下文，fn 为事务回调。
// 返回值：事务成功为空；任一写入错误触发回滚并原样返回。
func (r *GORMCommercialRecordRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}

// CreateRecord 用途：持久化一份人工资料。
// 参数：ctx 为事务上下文，value 为含真实操作者的台账。
// 返回值：成功为空；数据库约束失败时返回错误。
func (r *GORMCommercialRecordRepository) CreateRecord(ctx context.Context, value *CommercialRecord) error {
	return database.FromContext(ctx, r.db).Create(value).Error
}

// FindRecord 用途：按父商机和租户读取资料，可在更正及作废时加行锁。
// 参数：ctx 为上下文，tenant、opp 为父范围，id 为公开编号，lock 为是否当前读。
// 返回值：资料实体；跨范围或不存在统一返回不可见错误。
func (r *GORMCommercialRecordRepository) FindRecord(ctx context.Context, tenant string, opp uint64, id string, lock bool) (*CommercialRecord, error) {
	var value CommercialRecord
	db := database.FromContext(ctx, r.db).Where("tenant_id=? AND opportunity_id=? AND public_id=?", tenant, opp, id)
	if lock {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := db.Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCommercialRecordNotFound
	}
	return &value, err
}

// ListRecords 用途：按创建时间和主键倒序分页读取人工资料，保留已作废记录。
// 参数：ctx 为上下文，tenant、opp 为范围，page、size 为已校验分页。
// 返回值：资料列表、总数；数据库失败时返回错误。
func (r *GORMCommercialRecordRepository) ListRecords(ctx context.Context, tenant string, opp uint64, page, size int) ([]CommercialRecord, int64, error) {
	db := database.FromContext(ctx, r.db).Model(&CommercialRecord{}).
		Where("tenant_id=? AND opportunity_id=?", tenant, opp)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	values := []CommercialRecord{}
	err := db.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&values).Error
	return values, total, err
}

// SaveRecord 用途：仅更新人工资料自身字段，以乐观锁防止覆盖他人更正。
// 参数：ctx 为事务上下文，value 为新状态，expected 为客户端旧版本。
// 返回值：成功时推进实体版本；旧版本或数据库错误原样返回。
func (r *GORMCommercialRecordRepository) SaveRecord(ctx context.Context, value *CommercialRecord, expected uint64) error {
	result := database.FromContext(ctx, r.db).Model(&CommercialRecord{}).
		Where("id=? AND tenant_id=? AND opportunity_id=? AND version=?",
			value.ID, value.TenantID, value.OpportunityID, expected).
		Updates(map[string]any{
			"type": value.Type, "title": value.Title, "document_no": value.DocumentNo,
			"record_date": value.RecordDate, "amount": value.Amount, "status": value.Status,
			"notes": value.Notes, "is_void": value.IsVoid, "void_reason": value.VoidReason,
			"updated_by": value.UpdatedBy, "updated_by_name": value.UpdatedByName,
			"updated_at": value.UpdatedAt, "version": gorm.Expr("version+1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	value.Version = expected + 1
	return nil
}

// ResolveAttachments 用途：查询候选附件并在登记事务中锁住其信任元数据。
// 参数：ctx 为上下文，tenant、opp 为父范围，ids 为公开编号，lock 为是否加锁。
// 返回值：范围内附件；不存在的编号不返回，由业务层统一拒绝。
func (r *GORMCommercialRecordRepository) ResolveAttachments(ctx context.Context, tenant string, opp uint64, ids []string, lock bool) ([]Attachment, error) {
	values := []Attachment{}
	if len(ids) == 0 {
		return values, nil
	}
	db := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND opportunity_id=? AND public_id IN ?", tenant, opp, ids).
		Order("public_id ASC")
	if lock {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := db.Find(&values).Error
	return values, err
}

// ReplaceRecordAttachments 用途：在资料事务内替换关联，保持所选附件的展示顺序。
// 参数：ctx 为事务上下文，value 为台账，attachments 为已验证的可信附件。
// 返回值：成功为空；删除或写入关联失败时返回错误并触发回滚。
func (r *GORMCommercialRecordRepository) ReplaceRecordAttachments(ctx context.Context, value *CommercialRecord, attachments []Attachment) error {
	db := database.FromContext(ctx, r.db)
	if err := db.Where("tenant_id=? AND record_id=?", value.TenantID, value.ID).
		Delete(&commercialRecordAttachment{}).Error; err != nil {
		return err
	}
	if len(attachments) == 0 {
		return nil
	}
	links := make([]commercialRecordAttachment, 0, len(attachments))
	for i, attachment := range attachments {
		links = append(links, commercialRecordAttachment{
			TenantID: value.TenantID, RecordID: value.ID, AttachmentID: attachment.PublicID, Position: i,
		})
	}
	return db.Create(&links).Error
}

// ListRecordAttachments 用途：一次批量读取分页台账的附件，避免逐记录查询。
// 参数：ctx 为上下文，tenant、opp 为父范围，ids 为已读取台账的内部主键。
// 返回值：主键到附件列表的映射；数据库失败返回错误。
func (r *GORMCommercialRecordRepository) ListRecordAttachments(ctx context.Context, tenant string, opp uint64, ids []uint64) (map[uint64][]Attachment, error) {
	result := make(map[uint64][]Attachment, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		Attachment
		CommercialRecordID uint64
	}
	err := database.FromContext(ctx, r.db).Table("crm_opportunity_attachments AS a").
		Select("a.*, l.record_id AS commercial_record_id").
		Joins("JOIN crm_opportunity_commercial_record_attachments AS l ON l.tenant_id=a.tenant_id AND l.attachment_id=a.public_id").
		Where("l.tenant_id=? AND a.opportunity_id=? AND l.record_id IN ? AND a.deleted_at IS NULL", tenant, opp, ids).
		Order("l.record_id ASC,l.position ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.CommercialRecordID] = append(result[row.CommercialRecordID], row.Attachment)
	}
	return result, nil
}
