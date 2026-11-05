package importprovision

import "time"

// CustomerLookupRequest 按客户名称查询候选客户；名称由服务端归一化后精确匹配。
type CustomerLookupRequest struct {
	Name string `json:"name" binding:"required,max=200"`
}

// CustomerCandidate 是名称命中候选；Enabled=false 表示停用或已合并，合同侧应阻断该行。
type CustomerCandidate struct {
	ID      uint64 `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Enabled bool   `json:"enabled"`
}

// CustomerLookupResponse 返回候选集合与唯一命中标记；Unique 仅在恰好一个可用候选时为 true。
type CustomerLookupResponse struct {
	Origin         string              `json:"origin"`
	NormalizedName string              `json:"normalized_name"`
	Unique         bool                `json:"unique"`
	Candidates     []CustomerCandidate `json:"candidates"`
}

// CustomerContactInput 是通道创建客户时的联系人；必须恰好一个 is_registration=true。
type CustomerContactInput struct {
	Name           string `json:"name" binding:"required,max=100"`
	Phone          string `json:"phone" binding:"required,max=32"`
	Email          string `json:"email" binding:"omitempty,email,max=200"`
	IsRegistration bool   `json:"is_registration"`
}

// CustomerCreateRequest 是通道创建客户的请求体。
// owner_identity_id 为负责人（服务端仍用平台负责人目录复核）；三项主数据为空时填默认值。
type CustomerCreateRequest struct {
	Name             string                 `json:"name" binding:"required,max=200"`
	OwnerIdentityID  string                 `json:"owner_identity_id" binding:"required,max=64"`
	CustomerType     string                 `json:"customer_type,omitempty" binding:"omitempty,max=64"`
	Industry         string                 `json:"industry,omitempty" binding:"omitempty,max=64"`
	Region           string                 `json:"region,omitempty" binding:"omitempty,max=64"`
	Contacts         []CustomerContactInput `json:"contacts" binding:"required,min=1,max=20,dive"`
	ExternalRecordID string                 `json:"external_record_id,omitempty" binding:"omitempty,max=128"`
	Reason           string                 `json:"reason,omitempty" binding:"omitempty,max=500"`
}

// CustomerContactResponse 只回显脱敏联系人，机器通道不回传明文电话与邮箱。
type CustomerContactResponse struct {
	ID             uint64 `json:"id"`
	Name           string `json:"name"`
	PhoneMasked    string `json:"phone_masked"`
	EmailMasked    string `json:"email_masked"`
	IsRegistration bool   `json:"is_registration"`
}

// CustomerCreateResponse 是通道创建客户的响应体，origin 固定为 IMPORT_PROVISION。
type CustomerCreateResponse struct {
	Origin          string                    `json:"origin"`
	ID              uint64                    `json:"id"`
	CustomerNo      string                    `json:"customer_no"`
	Name            string                    `json:"name"`
	Status          string                    `json:"status"`
	CustomerType    string                    `json:"customer_type"`
	Industry        string                    `json:"industry"`
	Region          string                    `json:"region"`
	OwnerIdentityID string                    `json:"owner_identity_id"`
	OwnerOrgID      string                    `json:"owner_org_id"`
	Version         uint64                    `json:"version"`
	Contacts        []CustomerContactResponse `json:"contacts"`
	CreatedAt       time.Time                 `json:"created_at"`
}

// OpportunityCreateRequest 是通道创建商机的请求体。
// expected_sign_date 允许过去日期（历史合同的合同开始日）；普通创建仍要求不早于今天。
type OpportunityCreateRequest struct {
	Name               string `json:"name" binding:"required,max=200"`
	CustomerID         uint64 `json:"customer_id" binding:"required"`
	OwnerIdentityID    string `json:"owner_identity_id" binding:"required,max=64"`
	Type               string `json:"type,omitempty" binding:"omitempty,max=2000"`
	Source             string `json:"source,omitempty" binding:"omitempty,max=2000"`
	ExpectedAmount     string `json:"expected_amount" binding:"required,max=19"`
	ExpectedSignDate   string `json:"expected_sign_date" binding:"required,len=10"`
	RequirementSummary string `json:"requirement_summary" binding:"required,max=10000"`
	SystemCount        uint32 `json:"system_count"`
	PainPoints         string `json:"pain_points,omitempty" binding:"omitempty,max=10000"`
	CompetitorInfo     string `json:"competitor_info,omitempty" binding:"omitempty,max=10000"`
	ExternalRecordID   string `json:"external_record_id,omitempty" binding:"omitempty,max=128"`
	Reason             string `json:"reason,omitempty" binding:"omitempty,max=500"`
}

// OpportunityCreateResponse 是通道创建商机的响应体；创建后固定为初步接触 + FOLLOWING。
type OpportunityCreateResponse struct {
	Origin             string `json:"origin"`
	ID                 uint64 `json:"id"`
	OpportunityNo      string `json:"opportunity_no"`
	Name               string `json:"name"`
	CustomerID         uint64 `json:"customer_id"`
	Type               string `json:"type"`
	Source             string `json:"source"`
	ExpectedAmount     string `json:"expected_amount"`
	ExpectedSignDate   string `json:"expected_sign_date"`
	RequirementSummary string `json:"requirement_summary"`
	OwnerIdentityID    string `json:"owner_identity_id"`
	OwnerOrgID         string `json:"owner_org_id"`
	CurrentStage       string `json:"current_stage"`
	OppStatus          string `json:"opp_status"`
	Version            uint64 `json:"version"`
}

// OpportunitySignRequest 把商机推进到「已签约 + CLOSED」。
// contract_ref 必须通过当前赢单模式的验真前置；expected_version 为乐观锁版本。
type OpportunitySignRequest struct {
	ContractRef      string `json:"contract_ref" binding:"required,max=64"`
	ContractID       string `json:"contract_id,omitempty" binding:"omitempty,max=26"`
	ExpectedVersion  uint64 `json:"expected_version" binding:"required"`
	Reason           string `json:"reason" binding:"required,max=500"`
	ExternalRecordID string `json:"external_record_id,omitempty" binding:"omitempty,max=128"`
}

// OpportunitySignResponse 返回推进后的终态投影；IdempotentReplay=true 表示同合同引用的重复调用。
type OpportunitySignResponse struct {
	Origin           string  `json:"origin"`
	ID               uint64  `json:"id"`
	OpportunityNo    string  `json:"opportunity_no"`
	CustomerID       uint64  `json:"customer_id"`
	Name             string  `json:"name"`
	CurrentStage     string  `json:"current_stage"`
	OppStatus        string  `json:"opp_status"`
	ContractRef      *string `json:"contract_ref,omitempty"`
	Version          uint64  `json:"version"`
	IdempotentReplay bool    `json:"idempotent_replay"`
}
