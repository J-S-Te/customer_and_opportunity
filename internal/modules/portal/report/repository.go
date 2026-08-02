package report

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
func (r *GORMRepository) Create(ctx context.Context, value *Request) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) List(ctx context.Context, tenant string, customer uint64, pageNo, pageSize int) (pagination.Page[Request], error) {
	page := pagination.Page[Request]{Items: []Request{}, Page: pageNo, PageSize: pageSize}
	db := r.tx(ctx).Where("tenant_id=? AND customer_id=?", tenant, customer)
	if err := db.Model(&Request{}).Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := db.Order("submitted_at DESC,id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&page.Items).Error
	return page, err
}
func (r *GORMRepository) Find(ctx context.Context, tenant string, customer, id uint64) (*Request, error) {
	var value Request
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND id=?", tenant, customer, id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) FindByIdempotencyKey(ctx context.Context, tenant string, customer uint64, key string) (*Request, error) {
	var value Request
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND idempotency_key=?", tenant, customer, key).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) FindForUpdate(ctx context.Context, tenant string, id uint64) (*Request, error) {
	var value Request
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenant, id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) Update(ctx context.Context, value *Request, version uint64, fields map[string]any) error {
	fields["version"] = gorm.Expr("version + 1")
	res := r.tx(ctx).Model(&Request{}).Where("tenant_id=? AND id=? AND version=?", value.TenantID, value.ID, version).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return apperror.ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) CreateFile(ctx context.Context, value *File) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateIngestJob(ctx context.Context, value *IngestJob) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindIngestJobForUpdate(ctx context.Context, id uint64) (*IngestJob, error) {
	var value IngestJob
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) UpdateIngestJob(ctx context.Context, value *IngestJob, fields map[string]any) error {
	result := r.tx(ctx).Model(&IngestJob{}).
		Where("id=? AND status=? AND locked_by=?", value.ID, value.Status, value.LockedBy).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperror.ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) CreateOutbox(ctx context.Context, value *Outbox) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateStatusEvent(ctx context.Context, value *StatusEvent) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindStatusEventBySource(ctx context.Context, tenant string, requestID uint64, sourceKeyHash string) (*StatusEvent, error) {
	var value StatusEvent
	err := r.tx(ctx).
		Where("tenant_id=? AND request_id=? AND source_key_hash=?", tenant, requestID, sourceKeyHash).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}
func (r *GORMRepository) ListStatusEvents(ctx context.Context, tenant string, customer, requestID uint64) ([]StatusEvent, error) {
	values := make([]StatusEvent, 0)
	err := r.tx(ctx).
		Where("tenant_id=? AND customer_id=? AND request_id=?", tenant, customer, requestID).
		Order("sequence,id").Find(&values).Error
	return values, err
}
func (r *GORMRepository) CreateNotification(ctx context.Context, value *Notification) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) ListNotifications(ctx context.Context, actor Actor, unreadOnly bool, pageNo, pageSize int) (pagination.Page[NotificationView], error) {
	page := pagination.Page[NotificationView]{Items: []NotificationView{}, Page: pageNo, PageSize: pageSize}
	base := r.tx(ctx).Table("portal_report_notifications AS n").
		Joins("JOIN portal_report_requests AS r ON r.tenant_id=n.tenant_id AND r.customer_id=n.customer_id AND r.id=n.request_id AND r.deleted_at IS NULL").
		Where("n.tenant_id=? AND n.customer_id=? AND n.account_id=?", actor.TenantID, actor.CustomerID, actor.AccountID)
	if unreadOnly {
		base = base.Where("n.status=?", NotificationUnread)
	}
	if err := base.Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := base.Select("n.id,n.request_id,r.request_no,r.report_type,n.kind,n.status,n.created_at,n.read_at").
		Order("n.created_at DESC,n.id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&page.Items).Error
	return page, err
}
func (r *GORMRepository) CountUnreadNotifications(ctx context.Context, actor Actor) (int64, error) {
	var count int64
	err := r.tx(ctx).Model(&Notification{}).
		Where("tenant_id=? AND customer_id=? AND account_id=? AND status=?", actor.TenantID, actor.CustomerID, actor.AccountID, NotificationUnread).
		Count(&count).Error
	return count, err
}
func (r *GORMRepository) FindNotificationForUpdate(ctx context.Context, actor Actor, id uint64) (*Notification, error) {
	var value Notification
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND customer_id=? AND account_id=? AND id=?", actor.TenantID, actor.CustomerID, actor.AccountID, id).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotificationNotFound
	}
	return &value, err
}
func (r *GORMRepository) MarkNotificationRead(ctx context.Context, value *Notification, at time.Time) error {
	result := r.tx(ctx).Model(&Notification{}).
		Where("tenant_id=? AND customer_id=? AND account_id=? AND id=? AND status=?", value.TenantID, value.CustomerID, value.AccountID, value.ID, NotificationUnread).
		Updates(map[string]any{"status": NotificationRead, "read_at": &at})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperror.ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) CreateNotificationReadEvent(ctx context.Context, value *NotificationReadEvent) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindFile(ctx context.Context, tenant string, requestID uint64) (*File, error) {
	var value File
	err := r.tx(ctx).Where("tenant_id=? AND request_id=? AND deleted_at IS NULL", tenant, requestID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFileUnavailable
	}
	return &value, err
}
func (r *GORMRepository) RevokeActiveGrants(ctx context.Context, tenant string, customer, requestID uint64, accountID string, now time.Time) error {
	return r.tx(ctx).Model(&Grant{}).
		Where("tenant_id=? AND customer_id=? AND request_id=? AND account_id=? AND status=? AND active_slot='ACTIVE'", tenant, customer, requestID, accountID, GrantActive).
		Updates(map[string]any{"status": GrantRevoked, "active_slot": nil, "updated_by": accountID, "updated_at": now, "version": gorm.Expr("version + 1")}).Error
}
func (r *GORMRepository) CreateGrant(ctx context.Context, value *Grant) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindGrantByIssueKeyForUpdate(ctx context.Context, tenant string, customer, requestID uint64, accountID, issueKeyHash string) (*Grant, error) {
	var value Grant
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND customer_id=? AND request_id=? AND account_id=? AND issue_key_hash=?", tenant, customer, requestID, accountID, issueKeyHash).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrGrantNotFound
	}
	return &value, err
}
func (r *GORMRepository) FindGrantForUpdate(ctx context.Context, tenant string, customer, requestID uint64, accountID, tokenHash string) (*Grant, error) {
	var value Grant
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND customer_id=? AND request_id=? AND account_id=? AND token_hash=?", tenant, customer, requestID, accountID, tokenHash).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrGrantNotFound
	}
	return &value, err
}
func (r *GORMRepository) UpdateGrant(ctx context.Context, value *Grant, fields map[string]any) error {
	fields["version"] = gorm.Expr("version + 1")
	res := r.tx(ctx).Model(&Grant{}).Where("tenant_id=? AND id=? AND version=?", value.TenantID, value.ID, value.Version).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return apperror.ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) CreateDownloadEvent(ctx context.Context, value *DownloadEvent) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateDownloadEventOnce(ctx context.Context, value *DownloadEvent) error {
	return r.tx(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(value).Error
}

func (r *GORMRepository) CreateRiskAlert(ctx context.Context, value *RiskAlert) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) ListRiskAlerts(ctx context.Context, actor Actor, openOnly bool, pageNo, pageSize int) (pagination.Page[RiskAlertView], error) {
	page := pagination.Page[RiskAlertView]{Items: []RiskAlertView{}, Page: pageNo, PageSize: pageSize}
	base := r.riskAlertViewQuery(ctx).Where("a.tenant_id=? AND a.customer_id=? AND a.account_id=?", actor.TenantID, actor.CustomerID, actor.AccountID)
	if openOnly {
		base = base.Where("a.status=?", RiskAlertOpen)
	}
	if err := base.Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := base.Select(riskAlertViewColumns).Order("a.detected_at DESC,a.id DESC").
		Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&page.Items).Error
	return page, err
}

func (r *GORMRepository) ListRiskAlertsForReview(ctx context.Context, tenantID, status string, pageNo, pageSize int) (pagination.Page[RiskAlertView], error) {
	page := pagination.Page[RiskAlertView]{Items: []RiskAlertView{}, Page: pageNo, PageSize: pageSize}
	base := r.riskAlertViewQuery(ctx).Where("a.tenant_id=?", tenantID)
	if status != "" {
		base = base.Where("a.status=?", status)
	}
	if err := base.Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := base.Select(riskAlertViewColumns).Order("a.detected_at DESC,a.id DESC").
		Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&page.Items).Error
	return page, err
}

const riskAlertViewColumns = "a.public_id AS alert_id,a.request_id,r.request_no,r.report_type,a.account_id,a.risk_code,a.status,a.detected_at,a.acknowledged_at,a.resolved_at,a.resolved_by,a.resolution_action,a.resolution_reason,a.version"

func (r *GORMRepository) riskAlertViewQuery(ctx context.Context) *gorm.DB {
	return r.tx(ctx).Table("portal_report_risk_alerts AS a").
		Joins("JOIN portal_report_requests AS r ON r.tenant_id=a.tenant_id AND r.customer_id=a.customer_id AND r.id=a.request_id AND r.deleted_at IS NULL")
}

func (r *GORMRepository) FindRiskAlertForUpdate(ctx context.Context, tenantID, publicID string) (*RiskAlert, error) {
	var value RiskAlert
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND public_id=?", tenantID, publicID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRiskAlertNotFound
	}
	return &value, err
}

func (r *GORMRepository) FindRiskAlertView(ctx context.Context, tenantID, publicID string) (*RiskAlertView, error) {
	var value RiskAlertView
	err := r.riskAlertViewQuery(ctx).Where("a.tenant_id=? AND a.public_id=?", tenantID, publicID).
		Select(riskAlertViewColumns).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRiskAlertNotFound
	}
	return &value, err
}

func (r *GORMRepository) UpdateRiskAlert(ctx context.Context, value *RiskAlert, fields map[string]any) error {
	fields["version"] = gorm.Expr("version + 1")
	result := r.tx(ctx).Model(&RiskAlert{}).Where("tenant_id=? AND id=? AND version=? AND status=?", value.TenantID, value.ID, value.Version, value.Status).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperror.ErrVersionConflict
	}
	return nil
}

func (r *GORMRepository) CreateRiskReviewEvent(ctx context.Context, value *RiskReviewEvent) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) FindRiskReviewEvent(ctx context.Context, tenantID, actorID, idempotencyHash string) (*RiskReviewEvent, error) {
	var value RiskReviewEvent
	err := r.tx(ctx).Where("tenant_id=? AND actor_id=? AND idempotency_hash=?", tenantID, actorID, idempotencyHash).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRiskAlertNotFound
	}
	return &value, err
}

func (r *GORMRepository) FindGrantByIDForUpdate(ctx context.Context, tenantID string, id uint64) (*Grant, error) {
	var value Grant
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenantID, id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrGrantNotFound
	}
	return &value, err
}

func (r *GORMRepository) FindActiveGrantForUpdate(ctx context.Context, tenantID string, customerID, requestID uint64, accountID string) (*Grant, error) {
	var value Grant
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND customer_id=? AND request_id=? AND account_id=? AND status=? AND active_slot='ACTIVE'", tenantID, customerID, requestID, accountID, GrantActive).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrGrantNotFound
	}
	return &value, err
}
