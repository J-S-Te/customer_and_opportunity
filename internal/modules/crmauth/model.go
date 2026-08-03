package crmauth

import "time"

// LoginTransaction 持久化一次性 OIDC 登录事务。浏览器只持有 state 原文，数据库只保存其摘要；
// nonce 与 PKCE verifier 使用随机 nonce 的 AES-GCM 密文保存，既支持多实例回调，又避免数据库泄露后被直接复用。
type LoginTransaction struct {
	StateHash          string `gorm:"primaryKey;size:64"`
	TenantID           string `gorm:"size:64;not null;index"`
	NonceCipher        []byte `gorm:"type:varbinary(512);not null"`
	CodeVerifierCipher []byte `gorm:"type:varbinary(512);not null"`
	ReturnPath         string `gorm:"size:500;not null"`
	ExpiresAt          time.Time
	CreatedAt          time.Time
}

func (LoginTransaction) TableName() string { return "crm_oidc_login_transactions" }

// Session 是 CRM 浏览器登录态的权威记录。Cookie 原文不落库，只保存摘要；访问令牌因需要定期调用
// 平台 UserInfo 重验授权而加密保存，角色与权限快照不能脱离 AuthzRevision 单独信任。
type Session struct {
	SessionIDHash          string `gorm:"primaryKey;size:64"`
	TenantID               string `gorm:"size:64;not null;index"`
	PlatformUserID         string `gorm:"size:128;not null;index"`
	PersonID               string `gorm:"size:64;not null"`
	DisplayName            string `gorm:"size:200;not null"`
	PrimaryOrgID           string `gorm:"size:64;not null"`
	OrganizationIDsJSON    []byte `gorm:"type:json;not null"`
	RolesJSON              []byte `gorm:"type:json;not null"`
	PermissionsJSON        []byte `gorm:"type:json;not null"`
	RoleConfigHash         string `gorm:"size:128;not null"`
	AuthzRevision          uint64 `gorm:"not null"`
	AccessTokenCipher      []byte `gorm:"type:blob;not null"`
	ExpiresAt              time.Time
	AuthorizationCheckedAt time.Time
	CreatedAt              time.Time
	LastSeenAt             time.Time
	RevokedAt              *time.Time
}

func (Session) TableName() string { return "crm_oidc_sessions" }
