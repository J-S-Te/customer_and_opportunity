package customer

import (
	"encoding/json"
	"time"
)

const (
	QuickFilterKey         = "KEY"
	QuickFilterNew         = "NEW"
	QuickFilterWon         = "WON"
	QuickFilterFollowupDue = "FOLLOWUP_DUE"
)

type ContactInput struct {
	Name           string `json:"name" binding:"required,max=100"`
	Phone          string `json:"phone" binding:"required,max=32"`
	Email          string `json:"email" binding:"omitempty,email,max=200"`
	IsRegistration bool   `json:"is_registration"`
}

// UpdateContactInput 用指针区分敏感字段“未提交”和“提交空值”：未提交沿用原密文，
// 显式空邮箱表示清除，而电话不允许清空。
type UpdateContactInput struct {
	ID             uint64  `json:"id"`
	Name           string  `json:"name" binding:"required,max=100"`
	Phone          *string `json:"phone" binding:"omitempty,max=32"`
	Email          *string `json:"email" binding:"omitempty,email,max=200"`
	IsRegistration bool    `json:"is_registration"`
}

type CreateRequest struct {
	Name              string `json:"name" binding:"required,max=200"`
	UnifiedCreditCode string `json:"unified_credit_code" binding:"omitempty,max=64"`
	CustomerType      string `json:"customer_type" binding:"required,max=64"`
	Industry          string `json:"industry" binding:"required,max=64"`
	Region            string `json:"region" binding:"required,max=64"`
	// OwnerUserID 与 OwnerOrgID 仅为旧客户端兼容字段；创建服务始终使用认证主体及其主组织，
	// 不信任客户端指定的负责人。
	OwnerUserID             string         `json:"owner_user_id,omitempty" binding:"omitempty,max=64"`
	OwnerOrgID              string         `json:"owner_org_id" binding:"omitempty,max=64"`
	Contacts                []ContactInput `json:"contacts" binding:"required,min=1,dive"`
	DuplicateOverride       bool           `json:"duplicate_override"`
	DuplicateOverrideReason string         `json:"duplicate_override_reason" binding:"omitempty,max=500"`
	Reason                  string         `json:"reason" binding:"required,max=500"`
	IdempotencyKey          string         `json:"-"`
}

// UpdateRequest 全量替换可编辑主数据和联系人集合；Version 显式处理并发编辑，Reason 写入审计链路。
type UpdateRequest struct {
	Name                    string               `json:"name" binding:"required,max=200"`
	UnifiedCreditCode       *string              `json:"unified_credit_code" binding:"omitempty,max=64"`
	CustomerType            string               `json:"customer_type" binding:"required,max=64"`
	Industry                string               `json:"industry" binding:"required,max=64"`
	Region                  string               `json:"region" binding:"required,max=64"`
	OwnerUserID             string               `json:"owner_user_id" binding:"required,max=64"`
	OwnerOrgID              string               `json:"owner_org_id" binding:"omitempty,max=64"`
	Contacts                []UpdateContactInput `json:"contacts" binding:"required,min=1,dive"`
	DuplicateOverride       bool                 `json:"duplicate_override"`
	DuplicateOverrideReason string               `json:"duplicate_override_reason" binding:"omitempty,max=500"`
	Version                 uint64               `json:"version" binding:"required"`
	Reason                  string               `json:"reason" binding:"required,max=500"`
}

type StatusChangeRequest struct {
	Version uint64 `json:"version" binding:"required"`
	Reason  string `json:"reason" binding:"required,max=500"`
}

type MergeRequest struct {
	SourceCustomerID uint64 `json:"source_customer_id" binding:"required"`
	TargetCustomerID uint64 `json:"target_customer_id" binding:"required"`
	SourceVersion    uint64 `json:"source_version" binding:"required"`
	TargetVersion    uint64 `json:"target_version" binding:"required"`
	Reason           string `json:"reason" binding:"required,max=500"`
	IdempotencyKey   string `json:"-"`
}

type MergeMigrationCounts struct {
	Contacts      int64 `json:"contacts"`
	Stakeholders  int64 `json:"stakeholders"`
	Systems       int64 `json:"systems"`
	Followups     int64 `json:"followups"`
	Opportunities int64 `json:"opportunities"`
	PortalInvites int64 `json:"portal_invites"`
}

type StakeholderInput struct {
	ID                  uint64  `json:"id"`
	Name                string  `json:"name"`
	RoleTitle           string  `json:"role_title"`
	Influence           string  `json:"influence"`
	RelationshipSummary string  `json:"relationship_summary"`
	Phone               *string `json:"phone"`
	Email               *string `json:"email"`
}

type StakeholderResponse struct {
	ID                  uint64 `json:"id"`
	Name                string `json:"name"`
	RoleTitle           string `json:"role_title"`
	Influence           string `json:"influence"`
	RelationshipSummary string `json:"relationship_summary"`
	PhoneMasked         string `json:"phone"`
	EmailMasked         string `json:"email"`
	SortOrder           int    `json:"sort_order"`
	Version             uint64 `json:"version"`
}

type ReplaceStakeholdersRequest struct {
	Version uint64             `json:"version"`
	Reason  string             `json:"reason"`
	Items   []StakeholderInput `json:"items"`
}

type StakeholderCollectionResponse struct {
	CustomerVersion uint64                `json:"customer_version"`
	Items           []StakeholderResponse `json:"items"`
}

type InformationSystemInput struct {
	Name                string  `json:"name"`
	ProtectionLevel     string  `json:"protection_level"`
	ApplicationScenario string  `json:"application_scenario"`
	FilingNo            string  `json:"filing_no"`
	GradingDate         *string `json:"grading_date"`
	FilingStatus        string  `json:"filing_status"`
}

type InformationSystemResponse struct {
	ID                  uint64  `json:"id"`
	Name                string  `json:"name"`
	ProtectionLevel     string  `json:"protection_level"`
	ApplicationScenario string  `json:"application_scenario"`
	FilingNo            string  `json:"filing_no"`
	GradingDate         *string `json:"grading_date"`
	FilingStatus        string  `json:"filing_status"`
	SortOrder           int     `json:"sort_order"`
	Version             uint64  `json:"version"`
}

type ReplaceInformationSystemsRequest struct {
	Version uint64                   `json:"version"`
	Reason  string                   `json:"reason"`
	Items   []InformationSystemInput `json:"items"`
}

type InformationSystemCollectionResponse struct {
	CustomerVersion uint64                      `json:"customer_version"`
	Items           []InformationSystemResponse `json:"items"`
}

type MergeResponse struct {
	SourceCustomerID uint64               `json:"source_customer_id"`
	TargetCustomerID uint64               `json:"target_customer_id"`
	SourceStatus     string               `json:"source_status"`
	MergedIntoID     uint64               `json:"merged_into_id"`
	SourceVersion    uint64               `json:"source_version"`
	TargetVersion    uint64               `json:"target_version"`
	MigratedCounts   MergeMigrationCounts `json:"migrated_counts"`
	CompletedAt      time.Time            `json:"completed_at"`
}

type MergeBlocker struct {
	Code     string `json:"code"`
	Relation string `json:"relation"`
	Count    int64  `json:"count"`
	Message  string `json:"message"`
}

type DuplicateCheckRequest struct {
	Name              string `json:"name" binding:"required,max=200"`
	UnifiedCreditCode string `json:"unified_credit_code" binding:"omitempty,max=64"`
}

type DuplicateCandidate struct {
	ID         uint64 `json:"id"`
	CustomerNo string `json:"customer_no"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ExactCode  bool   `json:"exact_code"`
}

type ContactResponse struct {
	ID             uint64 `json:"id"`
	Name           string `json:"name"`
	PhoneMasked    string `json:"phone"`
	EmailMasked    string `json:"email,omitempty"`
	IsRegistration bool   `json:"is_registration"`
}

type Response struct {
	ID                   uint64            `json:"id"`
	CustomerNo           string            `json:"customer_no"`
	Name                 string            `json:"name"`
	CustomerType         string            `json:"customer_type"`
	Industry             string            `json:"industry"`
	Region               string            `json:"region"`
	OwnerUserID          string            `json:"owner_user_id"`
	OwnerDisplayName     string            `json:"owner_display_name,omitempty"`
	OwnerOrgID           string            `json:"owner_org_id"`
	Status               string            `json:"status"`
	CreditUpdatedAt      *time.Time        `json:"credit_updated_at,omitempty"`
	CreditLevel          string            `json:"credit_level"`
	EndDate              *string           `json:"end_date,omitempty"`
	MergedIntoID         *uint64           `json:"merged_into_id,omitempty"`
	Contacts             []ContactResponse `json:"contacts,omitempty"`
	Version              uint64            `json:"version"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	LastFollowupAt       *time.Time        `json:"last_followup_at,omitempty"`
	OpportunityAmountSum string            `json:"opportunity_amount_sum"`
}

type ListQuery struct {
	Keyword          string
	CustomerType     string
	Industry         string
	Region           string
	OwnerID          string
	Status           string
	CreditLevel      string
	QuickFilter      string
	CreatedFrom      *time.Time
	CreatedTo        *time.Time
	LastFollowupFrom *time.Time
	LastFollowupTo   *time.Time
	Now              time.Time
	Page             int
	PageSize         int
	SortBy           string
	SortOrder        string
}

type ChangeLogResponse struct {
	ID         uint64          `json:"id"`
	Operation  string          `json:"operation"`
	BeforeJSON json.RawMessage `json:"before"`
	AfterJSON  json.RawMessage `json:"after"`
	Reason     string          `json:"reason"`
	ActorID    string          `json:"actor_id"`
	Result     string          `json:"result"`
	RequestID  string          `json:"request_id"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// OpportunitySummary 有意省略需求正文、痛点、竞品情报以及合同/输单详情；
// 这些信息仍受商机详情权限边界保护，不能因查询客户历史而一并泄露。
type OpportunitySummary struct {
	ID             uint64    `json:"id"`
	OpportunityNo  string    `json:"opportunity_no"`
	Name           string    `json:"name"`
	ExpectedAmount string    `json:"expected_amount"`
	CurrentStage   string    `json:"current_stage"`
	Status         string    `json:"opp_status"`
	OwnerUserID    string    `json:"owner_user_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type FollowupCreateRequest struct {
	Type         string     `json:"type" binding:"required,oneof=PHONE VISIT EMAIL OTHER"`
	Content      string     `json:"content" binding:"required,max=10000"`
	FollowedAt   time.Time  `json:"followed_at" binding:"required"`
	NextFollowAt *time.Time `json:"next_follow_at"`
}

type ImportCommitRequest struct {
	Version uint64 `json:"version" binding:"required"`
}

type ImportRowIssue struct {
	Column  string `json:"column"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ImportPreviewRowResponse struct {
	RowNo                   uint32           `json:"row_no"`
	Status                  string           `json:"status"`
	Name                    string           `json:"name,omitempty"`
	UnifiedCreditCodeMasked string           `json:"unified_credit_code,omitempty"`
	CustomerType            string           `json:"customer_type,omitempty"`
	Industry                string           `json:"industry,omitempty"`
	Region                  string           `json:"region,omitempty"`
	OwnerUserID             string           `json:"owner_user_id,omitempty"`
	ContactName             string           `json:"contact_name,omitempty"`
	ContactPhoneMasked      string           `json:"contact_phone,omitempty"`
	ContactEmailMasked      string           `json:"contact_email,omitempty"`
	Issues                  []ImportRowIssue `json:"issues"`
}

type ImportPreviewResponse struct {
	JobNo          string                     `json:"job_no"`
	Status         string                     `json:"status"`
	Version        uint64                     `json:"version"`
	TotalRows      uint32                     `json:"total_rows"`
	ImportableRows uint32                     `json:"importable_rows"`
	WarningRows    uint32                     `json:"warning_rows"`
	ErrorRows      uint32                     `json:"error_rows"`
	ExpiresAt      time.Time                  `json:"expires_at"`
	Rows           []ImportPreviewRowResponse `json:"rows"`
}

type ImportCommitRowResponse struct {
	RowNo      uint32 `json:"row_no"`
	Status     string `json:"status"`
	CustomerID uint64 `json:"customer_id,omitempty"`
	CustomerNo string `json:"customer_no,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Message    string `json:"message,omitempty"`
}

type ImportCommitResponse struct {
	JobNo         string                    `json:"job_no"`
	Status        string                    `json:"status"`
	Version       uint64                    `json:"version"`
	TotalRows     uint32                    `json:"total_rows"`
	SucceededRows uint32                    `json:"succeeded_rows"`
	FailedRows    uint32                    `json:"failed_rows"`
	SkippedRows   uint32                    `json:"skipped_rows"`
	CompletedAt   time.Time                 `json:"completed_at"`
	Rows          []ImportCommitRowResponse `json:"rows"`
}

type FollowupResponse struct {
	ID           uint64     `json:"id"`
	Type         string     `json:"type"`
	Content      string     `json:"content"`
	FollowedAt   time.Time  `json:"followed_at"`
	FollowedBy   string     `json:"followed_by"`
	NextFollowAt *time.Time `json:"next_follow_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}
