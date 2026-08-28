package notification

import (
	"context"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
)

var (
	ErrNotFound    = apperror.New(404, "CRM_NOTIFICATION_NOT_FOUND", "notification not found")
	ErrNotReadable = apperror.New(409, "CRM_NOTIFICATION_NOT_READABLE", "notification cannot be marked as read")
)

type Response struct {
	ID              uint64     `json:"id"`
	Type            string     `json:"type"`
	OpportunityID   uint64     `json:"opportunity_id"`
	CustomerID      uint64     `json:"customer_id,omitempty"`
	OpportunityNo   string     `json:"opportunity_no"`
	OpportunityName string     `json:"opportunity_name"`
	RequestID       uint64     `json:"request_id,omitempty"`
	RequestNo       string     `json:"request_no,omitempty"`
	AssignmentID    uint64     `json:"assignment_id,omitempty"`
	ProgressID      uint64     `json:"progress_id,omitempty"`
	RecipientKind   string     `json:"recipient_kind"`
	Title           string     `json:"title"`
	Body            string     `json:"body"`
	TargetPath      string     `json:"target_path"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
}

type Page = pagination.Page[Response]

type Service struct {
	db  *gorm.DB
	now func() time.Time
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// ListMine 有意忽略 SELF/ORG/ALL 数据范围：商机可见范围扩大不能连带扩大个人收件箱，
// 每条通知仍必须按当前用户或人员主体精确匹配。
func (s *Service) ListMine(ctx context.Context, unreadOnly bool, page, pageSize int) (Page, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Page{}, err
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	query := personalInboxQuery(s.db.WithContext(ctx), principal)
	if unreadOnly {
		query = query.Where("status=?", StatusUnread)
	}
	var total int64
	if err = query.Count(&total).Error; err != nil {
		return Page{}, err
	}
	var items []Response
	err = query.Select("id,type,opportunity_id,customer_id,opportunity_no,opportunity_name,request_id,request_no,assignment_id,progress_id,recipient_kind,title,body,target_path,status,created_at,read_at").
		Order("created_at DESC,id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&items).Error
	return Page{Items: items, Page: page, PageSize: pageSize, Total: total}, err
}

func (s *Service) UnreadCount(ctx context.Context) (int64, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	err = personalInboxQuery(s.db.WithContext(ctx), principal).
		Where("status=?", StatusUnread).Count(&count).Error
	return count, err
}

func (s *Service) MarkRead(ctx context.Context, id uint64) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	result := personalInboxQuery(s.db.WithContext(ctx), principal).
		Where("id=? AND status=?", id, StatusUnread).
		Updates(map[string]any{"status": StatusRead, "read_at": now, "updated_at": now, "updated_by": principal.UserID, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	// 更新 0 行既可能是越权/不存在，也可能是并发请求已将同一通知标为已读。
	// 在相同个人收件箱边界内重读状态，使“标记已读”保持幂等且不泄露其他人的通知。
	var status string
	err = personalInboxQuery(s.db.WithContext(ctx), principal).Select("status").
		Where("id=?", id).
		Scan(&status).Error
	if err != nil || status == "" {
		return ErrNotFound
	}
	if status == StatusRead {
		return nil
	}
	return ErrNotReadable
}

func personalInboxQuery(db *gorm.DB, principal auth.Principal) *gorm.DB {
	query := db.Model(&Notification{}).
		Where("tenant_id=? AND status IN (?,?) AND deleted_at IS NULL", principal.TenantID, StatusUnread, StatusRead)
	var scopes []string
	var args []any
	if principal.HasPermission("opportunity.read") {
		scopes = append(scopes, "(type=? AND recipient_id=?)")
		args = append(args, TypeOpportunityOwnerChanged, principal.UserID)
	}
	if principal.HasPermission("customer.credit.read") {
		scopes = append(scopes, "(type IN (?,?,?,?,?,?) AND recipient_id=?)")
		args = append(args, TypeCreditRuleChanged, TypeCreditRuleCapReached, TypeCreditApplicationPending, TypeCreditApplicationApproved, TypeCreditApplicationRejected, TypeCreditApplicationInvalidated, principal.UserID)
	}
	if principal.HasPermission("presale.read") && principal.PersonID != "" {
		scopes = append(scopes, "(type IN (?,?,?) AND recipient_id=?)")
		args = append(args, TypePresaleAssigneeAdded, TypePresaleAssigneeRemoved, TypePresaleProgressAssignee, principal.PersonID)
	}
	if principal.HasPermission("presale.read") {
		scopes = append(scopes, "(type=? AND recipient_id=?)")
		args = append(args, TypePresaleProgressApplicant, principal.UserID)
		scopes = append(scopes, "(type IN (?,?,?) AND recipient_id=?)")
		args = append(args, TypePresaleApprovalPending, TypePresaleApprovalApproved, TypePresaleApprovalRejected, principal.UserID)
	}
	// 即使主体拥有售前权限，缺少平台签发的人员标识时也不能查看人员维度的指派通知；
	// 返回空集比拿用户 ID 猜测人员身份更安全。
	if len(scopes) == 0 {
		return query.Where("1=0")
	}
	return query.Where("("+strings.Join(scopes, " OR ")+")", args...)
}

func requirePrincipal(ctx context.Context) (auth.Principal, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok || principal.TenantID == "" || principal.UserID == "" {
		return auth.Principal{}, apperror.ErrUnauthenticated
	}
	if !principal.HasPermission("opportunity.read") && !principal.HasPermission("presale.read") && !principal.HasPermission("customer.credit.read") {
		return auth.Principal{}, apperror.ErrForbidden
	}
	return principal, nil
}
