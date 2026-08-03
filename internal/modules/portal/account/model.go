package account

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type IdentityStatus string

const (
	// 身份映射先保持待激活，只有完成 OIDC 主体校验与邀请消费后才可建立会话。
	IdentityPending  IdentityStatus = "PENDING"
	IdentityActive   IdentityStatus = "ACTIVE"
	// 禁用态是不可登录边界；切换状态时仓储会同步撤销该主体的活动会话。
	IdentityDisabled IdentityStatus = "DISABLED"
)

// IdentityLink 将平台 OIDC 主体绑定到租户内客户及可选联系人。
// PlatformUserID 是授权身份，DisplayName 仅供展示，二者不可相互替代。
type IdentityLink struct {
	database.Model
	AccountNo          string         `gorm:"size:32;not null;uniqueIndex:uq_portal_account_no,priority:2"`
	PlatformUserID     string         `gorm:"size:128;not null;uniqueIndex:uq_portal_platform_user,priority:2"`
	CustomerID         uint64         `gorm:"not null;index"`
	ContactID          *uint64        `gorm:"index"`
	Status             IdentityStatus `gorm:"size:16;not null;index"`
	DisplayName        string         `gorm:"size:200"`
	ActivatedAt        *time.Time     `gorm:"precision:3"`
	DisabledAt         *time.Time     `gorm:"precision:3"`
	LastClaimsRevision uint64         `gorm:"not null;default:0"`
	LastVerifiedAt     *time.Time     `gorm:"precision:3"`
}

func (IdentityLink) TableName() string { return "portal_identity_links" }

type Session struct {
	database.Model
	PublicID               string     `gorm:"size:64;not null;uniqueIndex"`
	SessionIDHash          string     `gorm:"size:64;not null;uniqueIndex"`
	PlatformUserID         string     `gorm:"size:128;not null;index"`
	CustomerID             uint64     `gorm:"not null;index"`
	AuthzRevision          uint64     `gorm:"not null"`
	RoleConfigHash         string     `gorm:"size:128;not null"`
	Roles                  []string   `gorm:"column:roles_json;serializer:json;type:json;not null"`
	Permissions            []string   `gorm:"column:permissions_json;serializer:json;type:json;not null"`
	AccessTokenCipher      []byte     `gorm:"type:blob;not null"`
	AuthorizationCheckedAt time.Time  `gorm:"precision:3;not null"`
	ExpiresAt              time.Time  `gorm:"precision:3;not null;index"`
	AbsoluteExpiry         time.Time  `gorm:"precision:3;not null"`
	LastSeenAt             time.Time  `gorm:"precision:3;not null"`
	RevokedAt              *time.Time `gorm:"precision:3;index"`
	IPHash                 string     `gorm:"size:64"`
	UserAgentHash          string     `gorm:"size:64"`
	IPMasked               string     `gorm:"size:64"`
	LocationSnapshot       string     `gorm:"size:128"`
	DeviceSnapshot         string     `gorm:"size:200"`
}

func (Session) TableName() string { return "portal_sessions" }

type ActivationContext struct {
	database.Model
	ContextIDHash          string     `gorm:"size:64;not null;uniqueIndex"`
	InviteTokenHash        string     `gorm:"size:64;not null;index"`
	InviteTokenCipher      []byte     `gorm:"type:varbinary(1024)"`
	ExpectedPlatformUserID string     `gorm:"size:128;not null"`
	CustomerID             uint64     `gorm:"not null;index"`
	StateHash              string     `gorm:"size:64;not null;uniqueIndex"`
	NonceHash              string     `gorm:"size:64;not null"`
	NonceCipher            []byte     `gorm:"type:varbinary(1024);not null"`
	PKCEVerifierCipher     []byte     `gorm:"type:varbinary(1024);not null"`
	ReturnPath             string     `gorm:"size:500;not null"`
	ExpiresAt              time.Time  `gorm:"precision:3;not null;index"`
	ConsumedAt             *time.Time `gorm:"precision:3"`
}

func (ActivationContext) TableName() string { return "portal_activation_contexts" }

type AuthEvent struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null;index"`
	PlatformUserID string    `gorm:"size:128;index"`
	CustomerID     *uint64   `gorm:"index"`
	Type           string    `gorm:"size:64;not null;index"`
	Result         string    `gorm:"size:16;not null"`
	ReasonCode     string    `gorm:"size:64"`
	RequestID      string    `gorm:"size:64;not null;index"`
	OccurredAt     time.Time `gorm:"precision:3;not null"`
}

func (AuthEvent) TableName() string { return "portal_auth_events" }

// SecurityEvent 是面向客户的最小化安全时间线。
// 它不保存令牌、Cookie、完整 IP 或其他账号标识；API 仅暴露 PublicID。
type SecurityEvent struct {
	database.Model
	PublicID         string     `gorm:"size:64;not null;uniqueIndex"`
	PlatformUserID   string     `gorm:"size:128;not null;index"`
	CustomerID       uint64     `gorm:"not null;index"`
	Type             string     `gorm:"size:64;not null;index"`
	RiskLevel        string     `gorm:"size:16;not null"`
	IPHash           string     `gorm:"size:64"`
	IPMasked         string     `gorm:"size:64"`
	LocationSnapshot string     `gorm:"size:128"`
	DeviceSnapshot   string     `gorm:"size:200"`
	ReasonCode       string     `gorm:"size:64"`
	OccurredAt       time.Time  `gorm:"precision:3;not null;index"`
	AcknowledgedAt   *time.Time `gorm:"precision:3"`
}

func (SecurityEvent) TableName() string { return "portal_security_events" }
