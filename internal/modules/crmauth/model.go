package crmauth

import "time"

// LoginTransaction persists the single-use OIDC state, nonce and PKCE verifier.
// Secret fields use randomized AES-GCM ciphertext and are never returned by an API.
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

// Session is the authoritative CRM browser session. Only a hash of the browser
// cookie is stored; access tokens are encrypted because they are needed for the
// platform UserInfo authorization re-check.
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
