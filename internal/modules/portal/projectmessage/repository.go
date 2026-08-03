package projectmessage

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

func NewGORMRepository(db *gorm.DB) *GORMRepository       { return &GORMRepository{db: db} }
func (r *GORMRepository) tx(ctx context.Context) *gorm.DB { return database.FromContext(ctx, r.db) }
func (r *GORMRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}

func (r *GORMRepository) LockCustomerAccount(ctx context.Context, actor CustomerActor) error {
	var id uint64
	err := r.tx(ctx).Table("portal_identity_links").Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND status='ACTIVE' AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, actor.AccountID).
		Take(&id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
func (r *GORMRepository) FindProjectRecipient(ctx context.Context, actor CustomerActor, projectID string) (ProjectRecipient, error) {
	var value ProjectRecipient
	err := r.tx(ctx).Table("portal_project_snapshots").
		Select("project_id,manager_name_snapshot AS manager_name,manager_portal_account_id").
		Where("tenant_id=? AND customer_id=? AND project_id=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, projectID).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProjectRecipient{}, ErrProjectNotFound
	}
	return value, err
}
func (r *GORMRepository) LockProjectRecipient(ctx context.Context, tenantID string, customerID uint64, projectID string) (ProjectRecipient, error) {
	var value ProjectRecipient
	err := r.tx(ctx).Table("portal_project_snapshots").Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("project_id,manager_name_snapshot AS manager_name,manager_portal_account_id").
		Where("tenant_id=? AND customer_id=? AND project_id=? AND deleted_at IS NULL", tenantID, customerID, projectID).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProjectRecipient{}, ErrProjectNotFound
	}
	return value, err
}
func (r *GORMRepository) FindCreateReplay(ctx context.Context, actor CustomerActor, key string) (*Conversation, error) {
	var value Conversation
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND customer_account_id=? AND create_idempotency_key=?", actor.TenantID, actor.CustomerID, actor.AccountID, key).Take(&value).Error
	return conversationResult(&value, err)
}
func (r *GORMRepository) FindByRecipient(ctx context.Context, actor CustomerActor, projectID, managerAccountID string) (*Conversation, error) {
	var value Conversation
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND project_id=? AND customer_account_id=? AND manager_account_id_snapshot=?", actor.TenantID, actor.CustomerID, projectID, actor.AccountID, managerAccountID).Take(&value).Error
	return conversationResult(&value, err)
}
func (r *GORMRepository) CreateConversation(ctx context.Context, value *Conversation) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) ListCustomer(ctx context.Context, actor CustomerActor, projectID string, pageNo, pageSize int) (pagination.Page[Conversation], error) {
	page := pagination.Page[Conversation]{Items: []Conversation{}, Page: pageNo, PageSize: pageSize}
	db := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND project_id=? AND customer_account_id=?", actor.TenantID, actor.CustomerID, projectID, actor.AccountID)
	if err := db.Model(&Conversation{}).Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Order("COALESCE(last_message_at,created_at) DESC,id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&page.Items).Error
	return page, err
}
func (r *GORMRepository) FindCurrentCustomerConversation(ctx context.Context, actor CustomerActor, projectID string) (*Conversation, error) {
	var value Conversation
	err := r.tx(ctx).Table("portal_project_conversations AS c").
		Joins("JOIN portal_project_snapshots AS p ON p.tenant_id=c.tenant_id AND p.customer_id=c.customer_id AND p.project_id=c.project_id AND p.manager_portal_account_id=c.manager_account_id_snapshot AND p.deleted_at IS NULL").
		Where("c.tenant_id=? AND c.customer_id=? AND c.project_id=? AND c.customer_account_id=?", actor.TenantID, actor.CustomerID, projectID, actor.AccountID).
		Select("c.*").Take(&value).Error
	return conversationResult(&value, err)
}
func (r *GORMRepository) FindCustomer(ctx context.Context, actor CustomerActor, publicID string) (*Conversation, error) {
	var value Conversation
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND customer_account_id=? AND public_id=?", actor.TenantID, actor.CustomerID, actor.AccountID, publicID).Take(&value).Error
	return conversationResult(&value, err)
}
func (r *GORMRepository) FindManager(ctx context.Context, actor ManagerActor, publicID string) (*Conversation, error) {
	var value Conversation
	err := r.tx(ctx).Table("portal_project_conversations AS c").
		Joins("JOIN portal_project_snapshots AS p ON p.tenant_id=c.tenant_id AND p.customer_id=c.customer_id AND p.project_id=c.project_id AND p.manager_portal_account_id=c.manager_account_id_snapshot AND p.deleted_at IS NULL").
		Where("c.tenant_id=? AND c.manager_account_id_snapshot=? AND c.public_id=?", actor.TenantID, actor.AccountID, publicID).
		Select("c.*").Take(&value).Error
	return conversationResult(&value, err)
}
func (r *GORMRepository) FindCustomerConversationByPublicID(ctx context.Context, actor CustomerActor, publicID string) (*Conversation, error) {
	var value Conversation
	err := r.tx(ctx).Table("portal_project_conversations AS c").
		Joins("JOIN portal_project_snapshots AS p ON p.tenant_id=c.tenant_id AND p.customer_id=c.customer_id AND p.project_id=c.project_id AND p.manager_portal_account_id=c.manager_account_id_snapshot AND p.deleted_at IS NULL").
		Where("c.tenant_id=? AND c.customer_id=? AND c.customer_account_id=? AND c.public_id=?", actor.TenantID, actor.CustomerID, actor.AccountID, publicID).
		Select("c.*").Take(&value).Error
	return conversationResult(&value, err)
}
func (r *GORMRepository) ListManager(ctx context.Context, actor ManagerActor, pageNo, pageSize int) (pagination.Page[Conversation], error) {
	page := pagination.Page[Conversation]{Items: []Conversation{}, Page: pageNo, PageSize: pageSize}
	db := r.tx(ctx).Table("portal_project_conversations AS c").
		Joins("JOIN portal_project_snapshots AS p ON p.tenant_id=c.tenant_id AND p.customer_id=c.customer_id AND p.project_id=c.project_id AND p.manager_portal_account_id=c.manager_account_id_snapshot AND p.deleted_at IS NULL").
		Where("c.tenant_id=? AND c.manager_account_id_snapshot=?", actor.TenantID, actor.AccountID)
	if err := db.Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Select("c.*").Order("COALESCE(c.last_message_at,c.created_at) DESC,c.id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&page.Items).Error
	return page, err
}
func (r *GORMRepository) FindForUpdate(ctx context.Context, tenantID string, id uint64) (*Conversation, error) {
	var value Conversation
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenantID, id).Take(&value).Error
	return conversationResult(&value, err)
}
func (r *GORMRepository) FindMessageReplay(ctx context.Context, tenantID, senderType, senderAccountID, key string) (*Message, error) {
	var value Message
	err := r.tx(ctx).Where("tenant_id=? AND sender_type=? AND sender_account_id=? AND idempotency_key=?", tenantID, senderType, senderAccountID, key).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) CreateMessage(ctx context.Context, value *Message) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CountRecent(ctx context.Context, tenantID string, conversationID uint64, senderType, senderAccountID string, since time.Time) (int64, error) {
	var count int64
	err := r.tx(ctx).Model(&Message{}).Where("tenant_id=? AND conversation_id=? AND sender_type=? AND sender_account_id=? AND accepted_at>=?", tenantID, conversationID, senderType, senderAccountID, since).Count(&count).Error
	return count, err
}
func (r *GORMRepository) ListMessages(ctx context.Context, tenantID string, conversationID uint64, before string, pageSize int) (MessagePage, error) {
	page := MessagePage{Items: []Message{}, PageSize: pageSize}
	db := r.tx(ctx).Where("tenant_id=? AND conversation_id=?", tenantID, conversationID)
	if err := db.Model(&Message{}).Count(&page.Total).Error; err != nil {
		return page, err
	}
	if before != "" {
		var anchor Message
		err := r.tx(ctx).Select("id,accepted_at").Where("tenant_id=? AND conversation_id=? AND message_cursor=?", tenantID, conversationID, before).Take(&anchor).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return page, ErrInvalidPageCursor
		}
		if err != nil {
			return page, err
		}
		db = db.Where("accepted_at<? OR (accepted_at=? AND id<?)", anchor.AcceptedAt, anchor.AcceptedAt, anchor.ID)
	}
	// 多取一行推导 has_more，避免 OFFSET；当前显示顺序的首项成为下一页稳定的排他游标。
	items := make([]Message, 0, pageSize+1)
	if err := db.Order("accepted_at DESC,id DESC").Limit(pageSize + 1).Find(&items).Error; err != nil {
		return page, err
	}
	if len(items) > pageSize {
		page.HasMore = true
		items = items[:pageSize]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	page.Items = items
	if page.HasMore && len(items) != 0 {
		page.NextBefore = items[0].Cursor
	}
	return page, nil
}
func (r *GORMRepository) FindRecipientMessage(ctx context.Context, tenantID string, conversationID uint64, messageCursor, recipientAccountID string) (*Message, error) {
	var value Message
	err := r.tx(ctx).Where("tenant_id=? AND conversation_id=? AND message_cursor=? AND recipient_account_id=?", tenantID, conversationID, messageCursor, recipientAccountID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidReadCursor
	}
	return &value, err
}
func (r *GORMRepository) FindMessageReadReceipt(ctx context.Context, tenantID string, conversationID uint64, readerType, readerAccountID string, messageID uint64) (*MessageReadReceipt, error) {
	var value MessageReadReceipt
	err := r.tx(ctx).Where("tenant_id=? AND conversation_id=? AND reader_type=? AND reader_account_id=? AND message_id=?", tenantID, conversationID, readerType, readerAccountID, messageID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) CreateMessageReadReceipt(ctx context.Context, value *MessageReadReceipt) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CountUnread(ctx context.Context, tenantID string, conversationID uint64, readerType, recipientAccountID string) (int64, error) {
	var count int64
	err := r.tx(ctx).Table("portal_project_messages AS m").
		Joins("LEFT JOIN portal_project_message_reads AS r ON r.tenant_id=m.tenant_id AND r.conversation_id=m.conversation_id AND r.message_id=m.id AND r.reader_type=? AND r.reader_account_id=?", readerType, recipientAccountID).
		Where("m.tenant_id=? AND m.conversation_id=? AND m.recipient_account_id=? AND r.id IS NULL", tenantID, conversationID, recipientAccountID).
		Count(&count).Error
	return count, err
}
func (r *GORMRepository) TouchConversation(ctx context.Context, value *Conversation, at time.Time) error {
	result := r.tx(ctx).Model(&Conversation{}).Where("tenant_id=? AND id=? AND version=?", value.TenantID, value.ID, value.Version).
		Updates(map[string]any{"last_message_at": &at, "updated_at": at, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	value.Version++
	value.LastMessageAt = &at
	return nil
}
func (r *GORMRepository) CreateEvent(ctx context.Context, value *Event) error {
	return r.tx(ctx).Create(value).Error
}

func conversationResult(value *Conversation, err error) (*Conversation, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return value, err
}
