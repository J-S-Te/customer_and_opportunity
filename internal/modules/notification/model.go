package notification

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

const (
	TypeOpportunityOwnerChanged      = "OPPORTUNITY_OWNER_CHANGED"
	TypeCreditRuleChanged            = "CREDIT_RULE_CHANGED"
	TypeCreditRuleCapReached         = "CREDIT_RULE_CAP_REACHED"
	TypeCreditApplicationPending     = "CREDIT_APPLICATION_PENDING"
	TypeCreditApplicationApproved    = "CREDIT_APPLICATION_APPROVED"
	TypeCreditApplicationRejected    = "CREDIT_APPLICATION_REJECTED"
	TypeCreditApplicationInvalidated = "CREDIT_APPLICATION_INVALIDATED"
	TypePresaleAssigneeAdded         = "PRESALE_ASSIGNEE_ADDED"
	TypePresaleAssigneeRemoved       = "PRESALE_ASSIGNEE_REMOVED"
	TypePresaleApprovalPending       = "PRESALE_APPROVAL_PENDING"
	TypePresaleApprovalApproved      = "PRESALE_APPROVAL_APPROVED"
	TypePresaleApprovalRejected      = "PRESALE_APPROVAL_REJECTED"
	TypePresaleProgressApplicant     = "PRESALE_PROGRESS_APPLICANT"
	TypePresaleProgressAssignee      = "PRESALE_PROGRESS_ASSIGNEE"
	StatusUnread                     = "UNREAD"
	StatusRead                       = "READ"
	StatusCancelled                  = "CANCELLED"
	RecipientPreviousOwner           = "PREVIOUS_OWNER"
	RecipientNewOwner                = "NEW_OWNER"
	RecipientAssigneeAdded           = "ASSIGNEE_ADDED"
	RecipientAssigneeRemoved         = "ASSIGNEE_REMOVED"
	RecipientApprovalPending         = "APPROVAL_PENDING"
	RecipientApprovalResult          = "APPROVAL_RESULT"
	RecipientProgressApplicant       = "PROGRESS_APPLICANT"
	RecipientProgressAssignee        = "PROGRESS_ASSIGNEE"
)

// Notification 是 CRM 站内通知的本地投影。写入此表只代表站内消息可见，
// 不代表邮件、短信或即时通信等外部渠道已经投递成功。
type Notification struct {
	database.Model
	SourceEventID       string     `gorm:"size:256;not null;uniqueIndex:uq_crm_notification_source,priority:2"`
	Type                string     `gorm:"size:64;not null;index"`
	OpportunityID       uint64     `gorm:"not null;index"`
	CustomerID          uint64     `gorm:"not null;index"`
	OpportunityVersion  uint64     `gorm:"not null"`
	OpportunityNo       string     `gorm:"size:32;not null"`
	OpportunityName     string     `gorm:"size:200;not null"`
	RequestID           uint64     `gorm:"not null;index"`
	RequestNo           string     `gorm:"size:32;not null"`
	AssignmentID        uint64     `gorm:"not null"`
	ProgressID          uint64     `gorm:"not null"`
	RecipientID         string     `gorm:"size:64;not null;index"`
	RecipientKind       string     `gorm:"size:32;not null"`
	Title               string     `gorm:"size:200;not null"`
	Body                string     `gorm:"size:500;not null"`
	TargetPath          string     `gorm:"size:500;not null"`
	Status              string     `gorm:"size:16;not null;index"`
	ReadAt              *time.Time `gorm:"precision:3"`
	PlatformDeliveredAt *time.Time `gorm:"precision:3"`
}

func (Notification) TableName() string { return "crm_notifications" }
