package evaluation

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Aggregate struct {
	Count           int64
	ProfessionalSum int64
	ResponseSum     int64
	ReportSum       int64
	AttitudeSum     int64
}

type LowScoreNoticeRow struct {
	NotificationID    uint64
	EvaluationID      uint64
	PublicID          string
	EvaluationNo      string
	ProjectID         string
	ProfessionalScore uint8
	ResponseScore     uint8
	ReportScore       uint8
	AttitudeScore     uint8
	AverageScore      string
	Comment           string
	Status            string
	CreatedAt         time.Time
	ReadAt            *time.Time
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository       { return &GORMRepository{db: db} }
func (r *GORMRepository) tx(ctx context.Context) *gorm.DB { return database.FromContext(ctx, r.db) }
func (r *GORMRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}

func (r *GORMRepository) FindByIdempotencyKey(ctx context.Context, actor Actor, key string) (*ServiceEvaluation, error) {
	var value ServiceEvaluation
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND account_id=? AND create_idempotency_key=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, actor.AccountID, key).Take(&value).Error
	return evaluationResult(&value, err)
}

func (r *GORMRepository) FindByProject(ctx context.Context, tenantID string, customerID uint64, projectID string) (*ServiceEvaluation, error) {
	var value ServiceEvaluation
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND project_id=? AND deleted_at IS NULL", tenantID, customerID, projectID).Take(&value).Error
	return evaluationResult(&value, err)
}

func (r *GORMRepository) FindOwned(ctx context.Context, actor Actor, publicID string) (*ServiceEvaluation, error) {
	var value ServiceEvaluation
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND account_id=? AND public_id=? AND deleted_at IS NULL", actor.TenantID, actor.CustomerID, actor.AccountID, publicID).Take(&value).Error
	return evaluationResult(&value, err)
}

func (r *GORMRepository) Create(ctx context.Context, value *ServiceEvaluation) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) CreateAuditLog(ctx context.Context, value *AuditLog) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) CreateAlert(ctx context.Context, value *Alert) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) CreateNotification(ctx context.Context, value *Notification) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) CreateOutbox(ctx context.Context, value *Outbox) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) Statistics(ctx context.Context, tenantID string) (Aggregate, error) {
	// 聚合查询只以租户为维度，不提供可组合的小群体筛选，配合服务层匿名样本阈值。
	var value Aggregate
	err := r.tx(ctx).Model(&ServiceEvaluation{}).
		Select("COUNT(*) AS count, COALESCE(SUM(professional_score),0) AS professional_sum, COALESCE(SUM(response_score),0) AS response_sum, COALESCE(SUM(report_score),0) AS report_sum, COALESCE(SUM(attitude_score),0) AS attitude_sum").
		Where("tenant_id=? AND status=? AND deleted_at IS NULL", tenantID, StatusSubmitted).
		Scan(&value).Error
	return value, err
}

func (r *GORMRepository) ListLowScoreNotices(ctx context.Context, tenantID, status string, pageNo, pageSize int) (pagination.Page[LowScoreNoticeRow], error) {
	page := pagination.Page[LowScoreNoticeRow]{Items: []LowScoreNoticeRow{}, Page: pageNo, PageSize: pageSize}
	base := r.tx(ctx).Table("portal_evaluation_notifications AS n").
		Joins("JOIN portal_service_evaluations AS e ON e.id=n.evaluation_id AND e.tenant_id=n.tenant_id AND e.deleted_at IS NULL").
		Where("n.tenant_id=? AND n.kind='LOW_SCORE'", tenantID)
	if status != "" {
		base = base.Where("n.status=?", status)
	}
	if err := base.Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := base.Select("n.id AS notification_id,n.evaluation_id,e.public_id,e.evaluation_no,e.project_id,e.professional_score,e.response_score,e.report_score,e.attitude_score,e.average_score,e.comment,n.status,n.created_at,n.read_at").
		Order("n.created_at DESC,n.id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&page.Items).Error
	return page, err
}

func (r *GORMRepository) FindLowScoreNoticeForUpdate(ctx context.Context, tenantID, publicEvaluationID string) (*LowScoreNoticeRow, error) {
	// 锁定通知与评价联接结果，使首次已读和审计事件在并发请求下仍只发生一次。
	var value LowScoreNoticeRow
	err := r.tx(ctx).Table("portal_evaluation_notifications AS n").Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("n.id AS notification_id,n.evaluation_id,e.public_id,e.evaluation_no,e.project_id,e.professional_score,e.response_score,e.report_score,e.attitude_score,e.average_score,e.comment,n.status,n.created_at,n.read_at").
		Joins("JOIN portal_service_evaluations AS e ON e.id=n.evaluation_id AND e.tenant_id=n.tenant_id AND e.deleted_at IS NULL").
		Where("n.tenant_id=? AND n.kind='LOW_SCORE' AND e.public_id=?", tenantID, publicEvaluationID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}

func (r *GORMRepository) MarkNoticeRead(ctx context.Context, tenantID string, notificationID uint64, actorID string, at time.Time) error {
	result := r.tx(ctx).Model(&Notification{}).
		Where("tenant_id=? AND id=? AND status='UNREAD'", tenantID, notificationID).
		Updates(map[string]any{"status": "READ", "read_at": &at, "read_by": actorID})
	return result.Error
}

func evaluationResult(value *ServiceEvaluation, err error) (*ServiceEvaluation, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return value, err
}
