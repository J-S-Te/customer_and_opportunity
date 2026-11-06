package localidentity

import "time"

type Record struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID  string    `gorm:"size:64;not null;index" json:"-"`
	Version   uint64    `gorm:"not null;default:1" json:"version"`
	CreatedBy string    `gorm:"size:128;not null" json:"-"`
	UpdatedBy string    `gorm:"size:128;not null" json:"-"`
	CreatedAt time.Time `gorm:"precision:3;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"precision:3;not null" json:"updated_at"`
}

type Authority struct {
	TenantID        string    `gorm:"primaryKey;size:64"`
	ApplicationCode string    `gorm:"primaryKey;size:64"`
	Mode            Mode      `gorm:"size:16;not null"`
	FirstUsedAt     time.Time `gorm:"precision:3;not null"`
	ActorID         string    `gorm:"size:128;not null"`
	Version         uint64    `gorm:"not null;default:1"`
}

// TableName 指定客户权威锁表。
// 参数：无。
// 返回值：固定表名。
func (Authority) TableName() string { return "portal_customer_authorities" }

type Customer struct {
	Record
	Code      string `gorm:"size:64;not null" json:"code"`
	Name      string `gorm:"size:200;not null" json:"name"`
	Status    string `gorm:"size:16;not null" json:"status"`
	Namespace string `gorm:"size:32;not null" json:"namespace"`
}

// TableName 指定本地客户目录表。
// 参数：无。
// 返回值：固定表名。
func (Customer) TableName() string { return "portal_local_customers" }

type Contact struct {
	Record
	CustomerID  uint64 `gorm:"not null;index" json:"customer_id"`
	DisplayName string `gorm:"size:128;not null" json:"display_name"`
	PhoneCipher []byte `gorm:"type:varbinary(1024);not null" json:"-"`
	PhoneDigest string `gorm:"size:64;not null" json:"-"`
	PhoneMasked string `gorm:"size:64;not null" json:"phone_masked"`
	Status      string `gorm:"size:16;not null" json:"status"`
}

// TableName 指定本地登记联系人表。
// 参数：无。
// 返回值：固定表名。
func (Contact) TableName() string { return "portal_local_contacts" }

type Invitation struct {
	Record
	InviteNo       string     `gorm:"size:64;not null;uniqueIndex" json:"invite_no"`
	CustomerID     uint64     `gorm:"not null;index" json:"customer_id"`
	ContactID      uint64     `gorm:"not null" json:"contact_id"`
	PlatformUserID string     `gorm:"size:128;not null;index" json:"platform_user_id"`
	AccountNo      string     `gorm:"size:64;not null" json:"account_no"`
	TokenHash      string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	TokenCipher    []byte     `gorm:"type:varbinary(1024);not null" json:"-"`
	Status         string     `gorm:"size:16;not null;index" json:"status"`
	ExpiresAt      time.Time  `gorm:"precision:3;not null" json:"expires_at"`
	ConsumedAt     *time.Time `gorm:"precision:3" json:"consumed_at,omitempty"`
	RevokedReason  string     `gorm:"size:500;not null" json:"revoked_reason,omitempty"`
}

// TableName 指定真实本地邀请台账，不复用CRM邀请状态。
// 参数：无。
// 返回值：固定表名。
func (Invitation) TableName() string { return "portal_local_invitations" }

type Operation struct {
	Record
	OperationNo    string     `gorm:"size:64;not null;uniqueIndex" json:"operation_no"`
	IdempotencyKey string     `gorm:"size:128;not null" json:"-"`
	RequestHash    string     `gorm:"size:64;not null" json:"-"`
	Kind           string     `gorm:"size:32;not null" json:"kind"`
	CustomerID     uint64     `gorm:"not null;index" json:"customer_id"`
	ContactID      uint64     `gorm:"not null" json:"contact_id,omitempty"`
	PlatformUserID string     `gorm:"size:128;not null" json:"platform_user_id,omitempty"`
	AccountNo      string     `gorm:"size:64;not null" json:"account_no,omitempty"`
	Stage          string     `gorm:"size:32;not null" json:"stage"`
	Status         string     `gorm:"size:16;not null" json:"status"`
	LastError      string     `gorm:"size:500;not null" json:"last_error,omitempty"`
	SnapshotCipher []byte     `gorm:"type:varbinary(4096);not null" json:"-"`
	ResultID       uint64     `gorm:"not null" json:"result_id,omitempty"`
	LeaseOwner     string     `gorm:"size:64;not null" json:"-"`
	LeaseUntil     *time.Time `gorm:"precision:3" json:"-"`
}

// TableName 指定管理员按原操作号续跑的持久化账本。
// 参数：无。
// 返回值：固定表名。
func (Operation) TableName() string { return "portal_local_operations" }
