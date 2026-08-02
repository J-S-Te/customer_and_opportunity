package account

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type IdentityStatus string

const (
	IdentityPending  IdentityStatus = "PENDING"
	IdentityActive   IdentityStatus = "ACTIVE"
	IdentityDisabled IdentityStatus = "DISABLED"
)

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

// SecurityEvent is the customer-visible, deliberately minimized security
// timeline. It never stores a token, cookie, full IP address or another
// account's identifier. PublicID is the only identifier exposed by the API.
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
