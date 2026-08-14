package portalinvite

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

const (
	StatusPending = "PENDING"
	StatusUsed    = "USED"
	StatusExpired = "EXPIRED"
	StatusRevoked = "REVOKED"
)

type Invite struct {
	database.Model
	InviteNo        string     `gorm:"size:32;not null;uniqueIndex"`
	CustomerID      uint64     `gorm:"not null;index"`
	ContactID       uint64     `gorm:"not null;index"`
	PlatformUserID  string     `gorm:"size:128;not null;index"`
	AccountNo       string     `gorm:"size:64;not null"`
	PortalAccountID string     `gorm:"size:64;not null"`
	TokenHash       string     `gorm:"size:64;not null;uniqueIndex"`
	Status          string     `gorm:"size:16;not null;index"`
	ExpiresAt       time.Time  `gorm:"precision:3;not null;index"`
	UsedAt          *time.Time `gorm:"precision:3"`
	RevokedAt       *time.Time `gorm:"precision:3"`
	RevokedReason   string     `gorm:"size:500"`
}

func (Invite) TableName() string { return "crm_portal_invites" }

type IdentityLink struct {
	database.Model
	CustomerID      uint64     `gorm:"not null;index"`
	ContactID       uint64     `gorm:"not null"`
	PlatformUserID  string     `gorm:"size:128;not null;index"`
	PortalAccountID string     `gorm:"size:64;not null"`
	Status          string     `gorm:"size:16;not null"`
	ProvisionedAt   time.Time  `gorm:"precision:3;not null"`
	LastVerifiedAt  *time.Time `gorm:"precision:3"`
}

func (IdentityLink) TableName() string { return "crm_portal_identity_links" }

const (
	OperationStagePrepared        = "PREPARED"
	OperationStageUserProvisioned = "USER_PROVISIONED"
	OperationStageRoleAssigned    = "ROLE_ASSIGNED"
	OperationStageMappingReady    = "MAPPING_READY"
	OperationStageCompleted       = "COMPLETED"
	OperationStatusProcessing     = "PROCESSING"
	OperationStatusRetryWait      = "RETRY_WAIT"
	OperationStatusCompleted      = "COMPLETED"
)

// ProvisionOperation 是生成邀请的持久化 Saga。ContactSnapshotCipher 冻结安全重试所需输入；
// TokenCipher 仅用于已完成的精确幂等重放返回原一次性链接，令牌明文不落库。
type ProvisionOperation struct {
	database.Model
	OperationNo           string     `gorm:"size:32;not null"`
	ActorID               string     `gorm:"size:128;not null"`
	IdempotencyKey        string     `gorm:"size:128;not null"`
	RequestHash           string     `gorm:"size:64;not null"`
	CustomerID            uint64     `gorm:"not null;index"`
	ContactID             uint64     `gorm:"not null"`
	ContactSnapshotCipher []byte     `gorm:"type:varbinary(4096);not null"`
	Stage                 string     `gorm:"size:32;not null;index"`
	Status                string     `gorm:"size:16;not null;index"`
	PlatformUserID        string     `gorm:"size:128;not null"`
	AccountNo             string     `gorm:"size:64;not null"`
	PortalAccountID       string     `gorm:"size:64;not null"`
	InviteID              *uint64    `gorm:"index"`
	TokenCipher           []byte     `gorm:"type:varbinary(1024)"`
	Attempts              uint16     `gorm:"not null;default:0"`
	LastErrorCode         string     `gorm:"size:64;not null"`
	LastErrorSummary      string     `gorm:"size:500;not null"`
	CompletedAt           *time.Time `gorm:"precision:3"`
}

func (ProvisionOperation) TableName() string { return "crm_portal_provision_operations" }

const (
	DisableStagePrepared        = "PREPARED"
	DisableStageMappingDisabled = "MAPPING_DISABLED"
	DisableStageCompleted       = "COMPLETED"
	DisableStatusProcessing     = "PROCESSING"
	DisableStatusRetryWait      = "RETRY_WAIT"
	DisableStatusCompleted      = "COMPLETED"
	DisableStatusDeadLetter     = "DEAD_LETTER"
)

// AccessDisableOperation 是只向前推进的持久化 Saga。先停用门户映射以关闭本地会话，
// 再撤销平台角色；每次重试沿用同一业务幂等键，但传输 nonce 每个请求都必须唯一。
type AccessDisableOperation struct {
	database.Model
	OperationNo         string     `gorm:"size:32;not null"`
	ActorID             string     `gorm:"size:128;not null"`
	IdempotencyKey      string     `gorm:"size:128;not null"`
	RequestHash         string     `gorm:"size:64;not null"`
	CustomerID          uint64     `gorm:"not null;index"`
	IdentityLinkID      uint64     `gorm:"not null;index"`
	IdentityLinkVersion uint64     `gorm:"not null"`
	ContactID           uint64     `gorm:"not null"`
	PlatformUserID      string     `gorm:"size:128;not null"`
	PortalAccountID     string     `gorm:"size:64;not null"`
	Reason              string     `gorm:"size:500;not null"`
	Stage               string     `gorm:"size:32;not null;index"`
	Status              string     `gorm:"size:16;not null;index"`
	Attempts            uint16     `gorm:"not null;default:0"`
	LastErrorCode       string     `gorm:"size:64;not null"`
	LastErrorSummary    string     `gorm:"size:500;not null"`
	NextRetryAt         *time.Time `gorm:"precision:3;index"`
	LockedBy            string     `gorm:"size:128;not null"`
	LockedUntil         *time.Time `gorm:"precision:3;index"`
	CompletedAt         *time.Time `gorm:"precision:3"`
}

func (AccessDisableOperation) TableName() string { return "crm_portal_access_disable_operations" }

const (
	CompensationPending    = "PENDING"
	CompensationProcessing = "PROCESSING"
	CompensationRetryWait  = "RETRY_WAIT"
	CompensationSucceeded  = "SUCCEEDED"
	CompensationDeadLetter = "DEAD_LETTER"
	CompensationRole       = "ASSIGN_PORTAL_ROLE"
	CompensationMapping    = "PROVISION_PORTAL_MAPPING"
	// CompensationBinding / CompensationBindingDisable 是平台客户绑定收敛（Phase 2 起）
	// 的补偿任务类型：平台 BIND/DISABLE_BIND 失败时入队，由补偿 worker 按幂等键补齐。
	CompensationBinding        = "BIND_PLATFORM_CUSTOMER"
	CompensationBindingDisable = "DISABLE_PLATFORM_CUSTOMER"
)

// CompensationTask 记录已经对外产生影响但尚未收敛的半完成状态。AccountNo 在任务创建时冻结
// 门户映射所需最小输入，任务不能从后续可变的客户/联系人敏感数据重建重试。
type CompensationTask struct {
	database.Model
	TaskNo           string     `gorm:"size:32;not null;uniqueIndex"`
	TaskType         string     `gorm:"size:64;not null;index"`
	CustomerID       uint64     `gorm:"not null;index"`
	ContactID        uint64     `gorm:"not null"`
	PlatformUserID   string     `gorm:"size:128;not null"`
	AccountNo        string     `gorm:"size:64;not null"`
	Status           string     `gorm:"size:16;not null;index"`
	Attempts         uint8      `gorm:"not null;default:0"`
	NextRetryAt      *time.Time `gorm:"precision:3;index"`
	LockedBy         string     `gorm:"size:128;not null"`
	LockedUntil      *time.Time `gorm:"precision:3;index"`
	LastAttemptAt    *time.Time `gorm:"precision:3"`
	CompletedAt      *time.Time `gorm:"precision:3"`
	LastErrorCode    string     `gorm:"size:64;not null"`
	LastErrorSummary string     `gorm:"size:500;not null"`
}

func (CompensationTask) TableName() string { return "crm_portal_compensation_tasks" }
