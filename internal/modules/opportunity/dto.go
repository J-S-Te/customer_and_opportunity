package opportunity

import "time"

type CreateRequest struct {
	Name               string `json:"name" binding:"required,max=200"`
	CustomerID         uint64 `json:"customer_id" binding:"required"`
	Type               string `json:"type" binding:"required,max=2000"`
	Source             string `json:"source" binding:"required,max=2000"`
	ExpectedAmount     string `json:"expected_amount" binding:"required"`
	ExpectedSignDate   string `json:"expected_sign_date" binding:"required"`
	RequirementSummary string `json:"requirement_summary" binding:"required"`
	SystemCount        uint32 `json:"system_count"`
	PainPoints         string `json:"pain_points" binding:"omitempty,max=10000"`
	CompetitorInfo     string `json:"competitor_info" binding:"omitempty,max=10000"`
	// OwnerUserID 与 OwnerOrgID 只用于兼容旧客户端；创建服务始终绑定认证主体及其主组织。
	OwnerUserID    string `json:"owner_user_id,omitempty" binding:"omitempty,max=64"`
	OwnerOrgID     string `json:"owner_org_id" binding:"omitempty,max=64"`
	IdempotencyKey string `json:"-"`
}

// UpdateRequest 只承载普通主数据字段；客户归属、负责人、阶段和生命周期变化必须走专用操作，
// 以应用更严格的权限、状态机与并发校验。
type UpdateRequest struct {
	Name               string `json:"name" binding:"required,max=200"`
	Type               string `json:"type" binding:"required,max=2000"`
	Source             string `json:"source" binding:"required,max=2000"`
	ExpectedAmount     string `json:"expected_amount" binding:"required"`
	ExpectedSignDate   string `json:"expected_sign_date" binding:"required"`
	RequirementSummary string `json:"requirement_summary" binding:"required,max=10000"`
	SystemCount        uint32 `json:"system_count"`
	PainPoints         string `json:"pain_points" binding:"omitempty,max=10000"`
	CompetitorInfo     string `json:"competitor_info" binding:"omitempty,max=10000"`
	Version            uint64 `json:"version" binding:"required"`
	Reason             string `json:"reason" binding:"required,max=500"`
}

type LifecycleRequest struct {
	Version uint64 `json:"version" binding:"required"`
	Reason  string `json:"reason" binding:"required,max=500"`
}

type ChangeOwnerRequest struct {
	OwnerUserID    string `json:"owner_user_id" binding:"required,max=64"`
	OwnerOrgID     string `json:"owner_org_id" binding:"omitempty,max=64"`
	Version        uint64 `json:"version" binding:"required"`
	Reason         string `json:"reason" binding:"required,max=500"`
	IdempotencyKey string `json:"-"`
}

type TeamMemberInput struct {
	UserID string `json:"user_id" binding:"required,max=64"`
	Role   string `json:"role" binding:"required,max=32"`
}

type ReplaceMembersRequest struct {
	Members []TeamMemberInput `json:"members" binding:"max=50,dive"`
	Version uint64            `json:"version" binding:"required"`
	Reason  string            `json:"reason" binding:"required,max=500"`
}

type MemberResponse struct {
	ID              uint64                       `json:"id"`
	UserID          string                       `json:"user_id"`
	DisplayName     string                       `json:"display_name,omitempty"`
	Organizations   []MemberOrganizationResponse `json:"organizations,omitempty"`
	DirectoryStatus string                       `json:"directory_status,omitempty"`
	Role            string                       `json:"role"`
	IsActive        bool                         `json:"is_active"`
	EndedAt         *time.Time                   `json:"ended_at,omitempty"`
	CreatedAt       time.Time                    `json:"created_at"`
	UpdatedAt       time.Time                    `json:"updated_at"`
}

type MemberOrganizationResponse struct {
	ID        string `json:"organization_id"`
	Name      string `json:"organization_name"`
	IsPrimary bool   `json:"is_primary"`
}

type TeamResponse struct {
	OpportunityID      uint64           `json:"opportunity_id"`
	Version            uint64           `json:"version"`
	DirectoryAvailable bool             `json:"directory_available"`
	Members            []MemberResponse `json:"members"`
}

type MemberTermResponse struct {
	ID               uint64     `json:"id"`
	UserID           string     `json:"user_id"`
	Role             string     `json:"role"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	SnapshotAt       *time.Time `json:"snapshot_at,omitempty"`
	ActiveAtSnapshot *bool      `json:"active_at_snapshot,omitempty"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	StartedBy        *string    `json:"started_by,omitempty"`
	EndedBy          *string    `json:"ended_by,omitempty"`
	SourceKind       string     `json:"source_kind"`
}

type MemberTermQuery struct {
	UserID     string
	ActiveOnly bool
	Page       int
	PageSize   int
}

type StageChangeRequest struct {
	TargetStage string  `json:"target_stage" binding:"required"`
	Reason      string  `json:"reason" binding:"required,max=500"`
	ContractRef *string `json:"contract_ref" binding:"omitempty,max=64"`
	LostReason  *string `json:"lost_reason" binding:"omitempty,max=64"`
	Version     uint64  `json:"version" binding:"required"`
}

type ExternalStatusRequest struct {
	OpportunityID uint64    `json:"opportunity_id" binding:"required"`
	Type          string    `json:"type" binding:"omitempty,max=16"`
	SourceID      string    `json:"source_id" binding:"required,max=64"`
	Status        string    `json:"status" binding:"required,max=32"`
	SourceAmount  *string   `json:"source_amount" binding:"omitempty"`
	ContractRef   *string   `json:"contract_ref" binding:"omitempty,max=64"`
	LostReason    *string   `json:"lost_reason" binding:"omitempty,max=64"`
	ChangedAt     time.Time `json:"changed_at" binding:"required"`
}

type ExternalStatusSnapshot struct {
	Type         string    `json:"type"`
	SourceID     string    `json:"source_id"`
	Status       string    `json:"status"`
	SourceAmount *string   `json:"source_amount,omitempty"`
	ContractRef  *string   `json:"contract_ref,omitempty"`
	LostReason   *string   `json:"lost_reason,omitempty"`
	ChangedAt    time.Time `json:"changed_at"`
}

type ExternalStatusResponse struct {
	OpportunityID    uint64                  `json:"opportunity_id"`
	Latest           *ExternalStatusSnapshot `json:"latest"`
	QuoteAmountCheck QuoteAmountCheck        `json:"quote_amount_check"`
}

const (
	QuoteAmountCheckNoApprovedQuote = "NO_APPROVED_QUOTE"
	QuoteAmountCheckAmountMissing   = "APPROVED_QUOTE_AMOUNT_MISSING"
	QuoteAmountCheckMatch           = "MATCH"
	QuoteAmountCheckMismatch        = "MISMATCH"
)

// QuoteAmountCheck 是读取时计算的投影，绑定商机版本和可信报价不可变快照；
// 不把它保存成可变告警状态，避免任一金额变化后遗留陈旧判断。
type QuoteAmountCheck struct {
	Status                 string     `json:"status"`
	Warning                bool       `json:"warning"`
	OpportunityVersion     uint64     `json:"opportunity_version"`
	ExpectedAmount         string     `json:"expected_amount"`
	ApprovedQuoteAmount    *string    `json:"approved_quote_amount,omitempty"`
	ApprovedQuoteSourceID  *string    `json:"approved_quote_source_id,omitempty"`
	ApprovedQuoteChangedAt *time.Time `json:"approved_quote_changed_at,omitempty"`
}

type ContractTransferRequest struct {
	Version        uint64 `json:"version" binding:"required"`
	Reason         string `json:"reason" binding:"required,max=500"`
	IdempotencyKey string `json:"-"`
}

// ContractTransferResponse 只确认 CRM 已持久化接收转合同发件箱事件，
// 不代表合同草稿已经生成，也不代表下游投递成功。
type ContractTransferResponse struct {
	OpportunityID  uint64 `json:"opportunity_id"`
	EventVersion   uint64 `json:"event_version"`
	EventID        string `json:"event_id"`
	DeliveryStatus string `json:"delivery_status"`
}

// ContractLinkRequest 是合同系统在完成接入核对后回写 CRM 的最小权威投影。
type ContractLinkRequest struct {
	EventID        string     `json:"event_id" binding:"required,max=128"`
	IntakeID       string     `json:"intake_id" binding:"required,max=64"`
	ContractID     string     `json:"contract_id" binding:"omitempty,max=26"`
	ContractNumber string     `json:"contract_number" binding:"required,max=64"`
	Status         string     `json:"status" binding:"required,oneof=LINK_CONFIRMED LINK_EXCEPTION"`
	LinkedAt       *time.Time `json:"linked_at"`
	SyncVersion    uint64     `json:"sync_version" binding:"required"`
}

type TerminalTodoRequest struct {
	ContractRef *string `json:"contract_ref" binding:"omitempty,max=64"`
	LostReason  *string `json:"lost_reason" binding:"omitempty,max=64"`
	Version     uint64  `json:"version" binding:"required"`
	Reason      string  `json:"reason" binding:"required,max=500"`
}

type FollowupCreateRequest struct {
	Type         string     `json:"type" binding:"required,max=32"`
	Content      string     `json:"content" binding:"required,max=10000"`
	FollowedAt   time.Time  `json:"followed_at" binding:"required"`
	NextFollowAt *time.Time `json:"next_follow_at"`
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

type StageHistoryResponse struct {
	ID          uint64    `json:"id"`
	FromStage   string    `json:"from_stage"`
	ToStage     string    `json:"to_stage"`
	Source      string    `json:"source"`
	Reason      string    `json:"reason"`
	ContractRef *string   `json:"contract_ref,omitempty"`
	LostReason  *string   `json:"lost_reason,omitempty"`
	PendingType string    `json:"pending_type"`
	OperatorID  string    `json:"operator_id"`
	ChangedAt   time.Time `json:"changed_at"`
	RequestID   string    `json:"request_id"`
}

type BoardColumn struct {
	Stage string     `json:"stage"`
	Items []Response `json:"items"`
}

type Response struct {
	ID                  uint64           `json:"id"`
	OpportunityNo       string           `json:"opportunity_no"`
	Name                string           `json:"name"`
	CustomerID          uint64           `json:"customer_id"`
	CustomerName        string           `json:"customer_name,omitempty"`
	Type                string           `json:"type"`
	Source              string           `json:"source"`
	ExpectedAmount      string           `json:"expected_amount"`
	ExpectedSignDate    string           `json:"expected_sign_date"`
	RequirementSummary  string           `json:"requirement_summary"`
	SystemCount         uint32           `json:"system_count"`
	PainPoints          string           `json:"pain_points"`
	CompetitorInfo      string           `json:"competitor_info"`
	OwnerUserID         string           `json:"owner_user_id"`
	OwnerOrgID          string           `json:"owner_org_id"`
	CurrentStage        string           `json:"current_stage"`
	Status              string           `json:"opp_status"`
	ContractRef         *string          `json:"contract_ref,omitempty"`
	ContractID          *string          `json:"contract_id,omitempty"`
	ContractIntakeID    *string          `json:"contract_intake_id,omitempty"`
	ContractLinkStatus  string           `json:"contract_link_status"`
	ContractLinkedAt    *time.Time       `json:"contract_linked_at,omitempty"`
	ContractSyncVersion uint64           `json:"contract_sync_version"`
	LostReason          *string          `json:"lost_reason,omitempty"`
	TerminalPendingType string           `json:"terminal_pending_type"`
	StageChangedAt      time.Time        `json:"stage_changed_at"`
	EndDate             *string          `json:"end_date,omitempty"`
	StatusBeforeVoid    *string          `json:"status_before_void,omitempty"`
	Version             uint64           `json:"version"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	Members             []MemberResponse `json:"members,omitempty"`
	SignedContractCount *uint64          `json:"signed_contract_count"`
}

type ListQuery struct {
	Keyword, Stage, Status, OwnerID string
	Page, PageSize                  int
	SortBy, SortOrder               string
}
