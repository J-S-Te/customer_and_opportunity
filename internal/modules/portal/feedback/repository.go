package feedback

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository       { return &GORMRepository{db: db} }
func (r *GORMRepository) tx(ctx context.Context) *gorm.DB { return database.FromContext(ctx, r.db) }
func (r *GORMRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}
func (r *GORMRepository) Create(ctx context.Context, value *Feedback) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindByCreateKey(ctx context.Context, actor CustomerActor, key string) (*Feedback, error) {
	var value Feedback
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND account_id=? AND create_idempotency_key=?", actor.TenantID, actor.CustomerID, actor.AccountID, key).Take(&value).Error
	return feedbackResult(&value, err)
}
func (r *GORMRepository) ListCustomer(ctx context.Context, actor CustomerActor, query ListQuery) (pagination.Page[Feedback], error) {
	page := pagination.Page[Feedback]{Items: []Feedback{}, Page: query.Page, PageSize: query.PageSize}
	db := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND account_id=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, actor.AccountID)
	if query.Status != "" {
		db = db.Where("status=?", query.Status)
	}
	if query.Type != "" {
		db = db.Where("type=?", query.Type)
	}
	if err := db.Model(&Feedback{}).Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Order("submitted_at DESC,id DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&page.Items).Error
	return page, err
}
func (r *GORMRepository) FindCustomer(ctx context.Context, actor CustomerActor, publicID string) (*Feedback, error) {
	var value Feedback
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND account_id=? AND public_id=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, actor.AccountID, publicID).Take(&value).Error
	return feedbackResult(&value, err)
}
func (r *GORMRepository) FindOperator(ctx context.Context, tenantID, publicID string) (*Feedback, error) {
	var value Feedback
	err := r.tx(ctx).Where("tenant_id=? AND public_id=? AND deleted_at IS NULL", tenantID, publicID).Take(&value).Error
	return feedbackResult(&value, err)
}
func (r *GORMRepository) ListOperator(ctx context.Context, tenantID string, query ListQuery) (pagination.Page[Feedback], error) {
	page := pagination.Page[Feedback]{Items: []Feedback{}, Page: query.Page, PageSize: query.PageSize}
	db := r.tx(ctx).Where("tenant_id=? AND deleted_at IS NULL", tenantID)
	if query.Status != "" {
		db = db.Where("status=?", query.Status)
	}
	if query.Type != "" {
		db = db.Where("type=?", query.Type)
	}
	if err := db.Model(&Feedback{}).Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Order("submitted_at DESC,id DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&page.Items).Error
	return page, err
}
func (r *GORMRepository) FindForUpdate(ctx context.Context, tenantID string, id uint64) (*Feedback, error) {
	var value Feedback
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=? AND deleted_at IS NULL", tenantID, id).Take(&value).Error
	return feedbackResult(&value, err)
}
func (r *GORMRepository) Update(ctx context.Context, value *Feedback, version uint64, fields map[string]any) error {
	// 乐观版本确保状态机转换不会覆盖另一位操作者刚提交的处理结果。
	fields["version"] = gorm.Expr("version+1")
	result := r.tx(ctx).Model(&Feedback{}).Where("tenant_id=? AND id=? AND version=? AND deleted_at IS NULL", value.TenantID, value.ID, version).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperror.ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) CreateMessage(ctx context.Context, value *Message) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindMessageByKey(ctx context.Context, tenantID string, feedbackID uint64, senderType, senderID, key string) (*Message, error) {
	// 消息幂等范围包含聚合和发送者，不能用一个账号的键重放另一个账号的正文。
	var value Message
	err := r.tx(ctx).Where("tenant_id=? AND feedback_id=? AND sender_type=? AND sender_id=? AND idempotency_key=?", tenantID, feedbackID, senderType, senderID, key).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) ListCustomerMessages(ctx context.Context, tenantID string, feedbackID uint64) ([]Message, error) {
	items := []Message{}
	err := r.tx(ctx).Where("tenant_id=? AND feedback_id=? AND visibility='CUSTOMER'", tenantID, feedbackID).Order("created_at,id").Find(&items).Error
	return items, err
}
func (r *GORMRepository) ListStatusLogs(ctx context.Context, tenantID string, feedbackID uint64) ([]StatusLog, error) {
	items := []StatusLog{}
	err := r.tx(ctx).Where("tenant_id=? AND feedback_id=?", tenantID, feedbackID).Order("occurred_at,id").Find(&items).Error
	return items, err
}
func (r *GORMRepository) FindStatusActionByKey(ctx context.Context, tenantID, key string) (*StatusLog, error) {
	var value StatusLog
	err := r.tx(ctx).Where("tenant_id=? AND idempotency_key=?", tenantID, key).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) CreateStatusLog(ctx context.Context, value *StatusLog) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateOutbox(ctx context.Context, value *Outbox) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateNotification(ctx context.Context, value *Notification) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) ListCustomerNotifications(ctx context.Context, actor CustomerActor, unreadOnly bool, pageNo, pageSize int) (pagination.Page[Notification], error) {
	page := pagination.Page[Notification]{Items: []Notification{}, Page: pageNo, PageSize: pageSize}
	db := r.tx(ctx).Table("portal_feedback_notifications n").Select("n.*, f.public_id, f.feedback_no").Joins("JOIN portal_feedbacks f ON f.id=n.feedback_id AND f.tenant_id=n.tenant_id").Where("n.tenant_id=? AND n.account_id=? AND f.customer_id=? AND f.deleted_at IS NULL", actor.TenantID, actor.AccountID, actor.CustomerID)
	if unreadOnly {
		db = db.Where("n.status=?", "UNREAD")
	}
	if err := db.Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Order("n.created_at DESC,n.id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&page.Items).Error
	return page, err
}
func (r *GORMRepository) CountUnreadCustomerNotifications(ctx context.Context, actor CustomerActor) (int64, error) {
	var count int64
	err := r.tx(ctx).Table("portal_feedback_notifications n").Joins("JOIN portal_feedbacks f ON f.id=n.feedback_id AND f.tenant_id=n.tenant_id").Where("n.tenant_id=? AND n.account_id=? AND f.customer_id=? AND f.deleted_at IS NULL AND n.status=?", actor.TenantID, actor.AccountID, actor.CustomerID, "UNREAD").Count(&count).Error
	return count, err
}
func (r *GORMRepository) FindCustomerNotificationForUpdate(ctx context.Context, actor CustomerActor, id uint64) (*Notification, error) {
	var value Notification
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Table("portal_feedback_notifications n").Select("n.*").Joins("JOIN portal_feedbacks f ON f.id=n.feedback_id AND f.tenant_id=n.tenant_id").Where("n.id=? AND n.tenant_id=? AND n.account_id=? AND f.customer_id=? AND f.deleted_at IS NULL", id, actor.TenantID, actor.AccountID, actor.CustomerID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) MarkCustomerNotificationRead(ctx context.Context, value *Notification, at time.Time) error {
	return r.tx(ctx).Model(&Notification{}).Where("id=? AND tenant_id=? AND account_id=? AND status=?", value.ID, value.TenantID, value.AccountID, "UNREAD").Updates(map[string]any{"status": "READ", "read_at": &at}).Error
}

func feedbackResult(value *Feedback, err error) (*Feedback, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return value, err
}
