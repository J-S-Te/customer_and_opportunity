// Package requestaudit 为每个 HTTP 请求持久化一个不可变投递
// 身份，并将已完成记录转发给基础平台审计服务。
package requestaudit

import "time"

const (
	StatusStarted    = "STARTED"
	StatusPending    = "PENDING"
	StatusProcessing = "PROCESSING"
	StatusRetry      = "RETRY"
	StatusDelivered  = "DELIVERED"
)

// Record 有意仅包含允许列表内的操作摘要。原始
// 路径、查询串、请求头、Cookie、请求体及响应体
// 绝不能持久化到请求审计 Outbox。
type Record struct {
	ID              uint64 `gorm:"primaryKey"`
	EventID         string `gorm:"size:64;not null;uniqueIndex"`
	TenantID        string `gorm:"size:64;not null;index"`
	ApplicationCode string `gorm:"size:64;not null"`
	EnvironmentCode string `gorm:"size:64;not null"`
	ActorType       string `gorm:"size:16;not null"`
	ActorID         string `gorm:"size:128;not null"`
	ActorName       string `gorm:"size:200;not null"`
	UserLoginIP     string `gorm:"size:45;not null"`
	// AUD-IP-2026：登录地址（UserLoginIP）固定于会话；ClientIP 是本次请求的操作地址；
	// IPSource 记录地址获取方式（TRUSTED_PROXY / DIRECT / LEGACY_PROTOCOL / UNKNOWN）。
	ClientIP       string     `gorm:"size:45;not null"`
	IPSource       string     `gorm:"size:32;not null"`
	Action         string     `gorm:"size:128;not null"`
	ResourceType   string     `gorm:"size:64;not null"`
	ResourceID     string     `gorm:"size:128;not null"`
	RequestID      string     `gorm:"size:128;not null;index"`
	Method         string     `gorm:"size:16;not null"`
	Route          string     `gorm:"size:500;not null"`
	HTTPStatus     int        `gorm:"not null"`
	Result         string     `gorm:"size:16;not null"`
	ReasonCode     string     `gorm:"size:128;not null"`
	RiskLevel      string     `gorm:"size:16;not null"`
	DeliveryStatus string     `gorm:"size:16;not null;index:idx_request_audit_delivery,priority:1"`
	Attempts       uint32     `gorm:"not null"`
	NextAttemptAt  *time.Time `gorm:"precision:3;index:idx_request_audit_delivery,priority:2"`
	LockedBy       string     `gorm:"size:128;not null"`
	LockedUntil    *time.Time `gorm:"precision:3"`
	LastErrorCode  string     `gorm:"size:128;not null"`
	OccurredAt     time.Time  `gorm:"precision:3;not null"`
	CompletedAt    *time.Time `gorm:"precision:3"`
	DeliveredAt    *time.Time `gorm:"precision:3"`
	CreatedAt      time.Time  `gorm:"precision:3;not null"`
	UpdatedAt      time.Time  `gorm:"precision:3;not null"`
}

func (Record) TableName() string { return "application_request_audit_outbox" }

type Start struct {
	EventID, TenantID, ApplicationCode, EnvironmentCode, RequestID, Method string
	OccurredAt                                                             time.Time
}

type Completion struct {
	ActorType, ActorID, ActorName, Action, Route string
	UserLoginIP                                  string
	ClientIP, IPSource                           string
	HTTPStatus                                   int
	Result, ReasonCode, RiskLevel                string
	CompletedAt                                  time.Time
}

// BusinessEvent 是允许列表内的业务变更摘要。详细的前后
// 快照保留在子系统受保护的审计表中。
type BusinessEvent struct {
	EventID, TenantID, ApplicationCode, EnvironmentCode string
	ActorType, ActorID, ActorName                       string
	UserLoginIP                                         string
	ClientIP, IPSource                                  string
	Action, ResourceType, ResourceID, RequestID         string
	Result, ReasonCode, RiskLevel                       string
	OccurredAt                                          time.Time
}

// OutboxStatus 是不含凭据的运维投影，有意仅包含
// 聚合信息：监控可识别投递积压及失败类别，
// 无需暴露审计载荷或用户请求详情。
type OutboxStatus struct {
	PendingCount        int64            `json:"pending_count"`
	RetryCount          int64            `json:"retry_count"`
	HeldCount           int64            `json:"held_count"` // 已计入 RetryCount；需要操作员解决。
	ProcessingCount     int64            `json:"processing_count"`
	StartedCount        int64            `json:"started_count"`
	DeliveredCount      int64            `json:"delivered_count"`
	OldestUndeliveredAt *time.Time       `json:"oldest_undelivered_at,omitempty"`
	MaxAttempts         uint32           `json:"max_attempts"`
	LastDeliveredAt     *time.Time       `json:"last_delivered_at,omitempty"`
	ErrorCounts         map[string]int64 `json:"error_counts"`
}
