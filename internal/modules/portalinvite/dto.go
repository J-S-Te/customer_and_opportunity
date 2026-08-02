package portalinvite

import "time"

type CreateResult struct {
	InviteNo        string    `json:"invite_no"`
	ActivationURL   string    `json:"activation_url"`
	Status          string    `json:"status"`
	ExpiresAt       time.Time `json:"expires_at"`
	ContactSummary  string    `json:"contact_summary"`
	IdentitySummary string    `json:"identity_summary"`
	LoginAccount    string    `json:"login_account"`
}

type CreateRequest struct {
	IdempotencyKey string `json:"-"`
}

type CurrentResult struct {
	InviteNo        string     `json:"invite_no"`
	Status          string     `json:"status"`
	ExpiresAt       time.Time  `json:"expires_at"`
	UsedAt          *time.Time `json:"used_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	ContactSummary  string     `json:"contact_summary"`
	IdentitySummary string     `json:"identity_summary"`
	LoginAccount    string     `json:"login_account"`
	Version         uint64     `json:"version"`
}

type VerifiedResult struct {
	TenantID        string    `json:"tenant_id"`
	CustomerID      uint64    `json:"customer_id"`
	ContactID       *uint64   `json:"contact_id,omitempty"`
	PlatformUserID  string    `json:"platform_user_id"`
	PortalAccountID string    `json:"portal_account_id"`
	ExpireAt        time.Time `json:"expire_at"`
}

type RevokeRequest struct {
	Reason  string `json:"reason" binding:"required,max=500"`
	Version uint64 `json:"version" binding:"required"`
}

type VerifyRequest struct {
	Token string `json:"token" binding:"required"`
}

type ConsumeRequest struct {
	Token          string `json:"token" binding:"required"`
	PlatformUserID string `json:"platform_user_id" binding:"required,max=128"`
	RequestID      string `json:"request_id" binding:"required,max=64"`
}

type DisableAccessRequest struct {
	Reason         string `json:"reason" binding:"required,max=500"`
	IdempotencyKey string `json:"-"`
}

type DisableAccessResult struct {
	CustomerID  uint64     `json:"customer_id"`
	Status      string     `json:"status"`
	OperationNo string     `json:"operation_no"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type AccessStatusResult struct {
	CustomerID       uint64     `json:"customer_id"`
	AccessStatus     string     `json:"access_status"`
	OperationNo      string     `json:"operation_no,omitempty"`
	OperationStatus  string     `json:"operation_status,omitempty"`
	OperationStage   string     `json:"operation_stage,omitempty"`
	LastErrorCode    string     `json:"last_error_code,omitempty"`
	LastErrorSummary string     `json:"last_error_summary,omitempty"`
	NextRetryAt      *time.Time `json:"next_retry_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}
