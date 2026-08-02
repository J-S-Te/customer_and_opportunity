package notification

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

const (
	TypeOpportunityOwnerChanged  = "OPPORTUNITY_OWNER_CHANGED"
	TypePresaleAssigneeAdded     = "PRESALE_ASSIGNEE_ADDED"
	TypePresaleAssigneeRemoved   = "PRESALE_ASSIGNEE_REMOVED"
	TypePresaleProgressApplicant = "PRESALE_PROGRESS_APPLICANT"
	TypePresaleProgressAssignee  = "PRESALE_PROGRESS_ASSIGNEE"
	StatusUnread                 = "UNREAD"
	StatusRead                   = "READ"
	StatusCancelled              = "CANCELLED"
	RecipientPreviousOwner       = "PREVIOUS_OWNER"
	RecipientNewOwner            = "NEW_OWNER"
	RecipientAssigneeAdded       = "ASSIGNEE_ADDED"
	RecipientAssigneeRemoved     = "ASSIGNEE_REMOVED"
	RecipientProgressApplicant   = "PROGRESS_APPLICANT"
	RecipientProgressAssignee    = "PROGRESS_ASSIGNEE"
)

// Notification is the CRM-local in-product projection. Delivery into this
// table does not imply delivery to an external IM, email, or SMS channel.
type Notification struct {
	database.Model
	SourceEventID      string     `gorm:"size:64;not null;uniqueIndex:uq_crm_notification_source,priority:2"`
	Type               string     `gorm:"size:64;not null;index"`
	OpportunityID      uint64     `gorm:"not null;index"`
	OpportunityVersion uint64     `gorm:"not null"`
	OpportunityNo      string     `gorm:"size:32;not null"`
	OpportunityName    string     `gorm:"size:200;not null"`
	RequestID          uint64     `gorm:"not null;index"`
	RequestNo          string     `gorm:"size:32;not null"`
	AssignmentID       uint64     `gorm:"not null"`
	ProgressID         uint64     `gorm:"not null"`
	RecipientID        string     `gorm:"size:64;not null;index"`
	RecipientKind      string     `gorm:"size:32;not null"`
	Title              string     `gorm:"size:200;not null"`
	Body               string     `gorm:"size:500;not null"`
	TargetPath         string     `gorm:"size:500;not null"`
	Status             string     `gorm:"size:16;not null;index"`
	ReadAt             *time.Time `gorm:"precision:3"`
}

func (Notification) TableName() string { return "crm_notifications" }
