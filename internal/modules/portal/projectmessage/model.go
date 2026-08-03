package projectmessage

import "time"

const (
	SenderCustomer = "CUSTOMER"
	SenderManager  = "MANAGER"
)

// Conversation 是 Portal 自有的站内信会话。收件人必须是项目源提供的权威账号标识，展示名和人员引用不能用于投递身份。
type Conversation struct {
	ID                       uint64     `gorm:"primaryKey;autoIncrement" json:"-"`
	PublicID                 string     `gorm:"size:64;not null;uniqueIndex" json:"id"`
	TenantID                 string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project_conversation,priority:1;uniqueIndex:uq_portal_project_conversation_create,priority:1" json:"-"`
	CustomerID               uint64     `gorm:"not null;uniqueIndex:uq_portal_project_conversation,priority:2" json:"-"`
	ProjectID                string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project_conversation,priority:3" json:"project_id"`
	CustomerAccountID        string     `gorm:"size:128;not null;uniqueIndex:uq_portal_project_conversation,priority:4;uniqueIndex:uq_portal_project_conversation_create,priority:2" json:"-"`
	ManagerAccountIDSnapshot string     `gorm:"size:128;not null;uniqueIndex:uq_portal_project_conversation,priority:5" json:"-"`
	ManagerNameSnapshot      string     `gorm:"size:128;not null;default:''" json:"manager_name"`
	CreateIdempotencyKey     string     `gorm:"size:128;not null;uniqueIndex:uq_portal_project_conversation_create,priority:3" json:"-"`
	CreateRequestHash        string     `gorm:"size:64;not null" json:"-"`
	LastMessageAt            *time.Time `gorm:"precision:3;index" json:"last_message_at,omitempty"`
	CreatedAt                time.Time  `gorm:"precision:3;not null" json:"created_at"`
	UpdatedAt                time.Time  `gorm:"precision:3;not null" json:"updated_at"`
	Version                  uint64     `gorm:"not null;default:1" json:"version"`
}

func (Conversation) TableName() string { return "portal_project_conversations" }

type Message struct {
	ID                 uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Cursor             string    `gorm:"column:message_cursor;size:64;not null;uniqueIndex" json:"cursor"`
	TenantID           string    `gorm:"size:64;not null;uniqueIndex:uq_portal_project_message_key,priority:1" json:"-"`
	ConversationID     uint64    `gorm:"not null;index:idx_portal_project_message_timeline,priority:2" json:"-"`
	SenderType         string    `gorm:"size:16;not null;uniqueIndex:uq_portal_project_message_key,priority:2" json:"sender_type"`
	SenderAccountID    string    `gorm:"size:128;not null;uniqueIndex:uq_portal_project_message_key,priority:3" json:"-"`
	RecipientAccountID string    `gorm:"size:128;not null" json:"-"`
	Content            string    `gorm:"type:text;not null" json:"content"`
	IdempotencyKey     string    `gorm:"size:128;not null;uniqueIndex:uq_portal_project_message_key,priority:4" json:"-"`
	RequestHash        string    `gorm:"size:64;not null" json:"-"`
	AcceptedAt         time.Time `gorm:"precision:3;not null;index:idx_portal_project_message_timeline,priority:3" json:"accepted_at"`
}

func (Message) TableName() string { return "portal_project_messages" }

// MessageReadReceipt 逐条记录客户端实际展示的收件消息；相比高水位，它不会把旧分页中的未读空洞静默标记为已读。
type MessageReadReceipt struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID        string    `gorm:"size:64;not null;uniqueIndex:uq_portal_project_message_read,priority:1"`
	ConversationID  uint64    `gorm:"not null;uniqueIndex:uq_portal_project_message_read,priority:2;index:idx_portal_project_message_read_conversation,priority:2"`
	MessageID       uint64    `gorm:"not null;uniqueIndex:uq_portal_project_message_read,priority:5;index:idx_portal_project_message_read_message"`
	ReaderType      string    `gorm:"size:16;not null;uniqueIndex:uq_portal_project_message_read,priority:3"`
	ReaderAccountID string    `gorm:"size:128;not null;uniqueIndex:uq_portal_project_message_read,priority:4"`
	ReadAt          time.Time `gorm:"precision:3;not null"`
	CreatedAt       time.Time `gorm:"precision:3;not null"`
}

func (MessageReadReceipt) TableName() string { return "portal_project_message_reads" }

type Event struct {
	ID                 uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID           string    `gorm:"size:64;not null;index"`
	ConversationID     uint64    `gorm:"not null;index"`
	MessageID          *uint64   `gorm:"index"`
	Operation          string    `gorm:"size:64;not null"`
	ActorType          string    `gorm:"size:16;not null"`
	ActorAccountID     string    `gorm:"size:128;not null"`
	RecipientAccountID string    `gorm:"size:128;not null;default:''"`
	RequestID          string    `gorm:"size:64;not null;default:''"`
	Result             string    `gorm:"size:16;not null"`
	OccurredAt         time.Time `gorm:"precision:3;not null"`
}

func (Event) TableName() string { return "portal_project_message_events" }
