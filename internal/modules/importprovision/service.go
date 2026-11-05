package importprovision

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
)

// Origin 是本通道写入审计与来源标记的固定取值。
const Origin = customer.ProvisionOrigin

// CustomerProvisioner 是客户侧通道能力。
// 由 customer.Service 实现：它不会回退到认证主体作为负责人，也不放宽普通创建规则。
type CustomerProvisioner interface {
	LookupProvisionCandidates(ctx context.Context, name string) (customer.ProvisionLookup, error)
	CreateProvision(ctx context.Context, request customer.ProvisionCreateRequest) (*customer.Response, error)
}

// OpportunityProvisioner 是商机侧通道能力。
// 由 opportunity.Service 实现：创建固定为初步接触 + FOLLOWING，推进必须走既有状态机与赢单前置。
type OpportunityProvisioner interface {
	CreateProvision(ctx context.Context, request opportunity.ProvisionCreateRequest) (*opportunity.Response, error)
	// SignProvision 的第二个返回值表示“同合同引用重复调用已回放既有结果”。
	SignProvision(ctx context.Context, id uint64, request opportunity.ProvisionSignRequest) (*opportunity.Response, bool, error)
}

// Service 编排四个机器端点，并把领域结果裁剪成稳定的机器契约。
type Service struct {
	customers     CustomerProvisioner
	opportunities OpportunityProvisioner
}

// NewService 装配通道服务；任一依赖缺失时端点按 503 拒绝，而不是静默降级。
func NewService(customers CustomerProvisioner, opportunities OpportunityProvisioner) *Service {
	return &Service{customers: customers, opportunities: opportunities}
}

func (s *Service) available() error {
	if s == nil || s.customers == nil || s.opportunities == nil {
		return ErrUnavailable
	}
	return nil
}

// LookupCustomer 按名称精确查询候选客户。
// 参数：ctx 携带机器身份，request 为名称入参。
// 返回值：候选、唯一命中标记与归一化名称。
func (s *Service) LookupCustomer(ctx context.Context, request CustomerLookupRequest) (CustomerLookupResponse, error) {
	if err := s.available(); err != nil {
		return CustomerLookupResponse{}, err
	}
	lookup, err := s.customers.LookupProvisionCandidates(ctx, request.Name)
	if err != nil {
		return CustomerLookupResponse{}, err
	}
	candidates := make([]CustomerCandidate, 0, len(lookup.Candidates))
	for _, candidate := range lookup.Candidates {
		candidates = append(candidates, CustomerCandidate{ID: candidate.ID, Name: candidate.Name, Status: candidate.Status, Enabled: candidate.Enabled})
	}
	return CustomerLookupResponse{Origin: Origin, NormalizedName: lookup.NormalizedName, Unique: lookup.Unique, Candidates: candidates}, nil
}

// CreateCustomer 在通道内创建客户。
// 参数：ctx 携带机器身份，actorIdentityID 为审计归属（X-Actor-Identity-ID），
// idempotencyKey 为必需的幂等键，request 为通道入参。
// 返回值：创建或幂等重放的客户投影。
func (s *Service) CreateCustomer(ctx context.Context, actorIdentityID, idempotencyKey string, request CustomerCreateRequest) (CustomerCreateResponse, error) {
	if err := s.available(); err != nil {
		return CustomerCreateResponse{}, err
	}
	contacts := make([]customer.ContactInput, 0, len(request.Contacts))
	for _, contact := range request.Contacts {
		contacts = append(contacts, customer.ContactInput{Name: contact.Name, Phone: contact.Phone, Email: contact.Email, IsRegistration: contact.IsRegistration})
	}
	created, err := s.customers.CreateProvision(ctx, customer.ProvisionCreateRequest{
		Name: request.Name, CustomerType: request.CustomerType, Industry: request.Industry, Region: request.Region,
		OwnerIdentityID: request.OwnerIdentityID, Contacts: contacts, ActorIdentityID: actorIdentityID,
		ExternalRecordID: request.ExternalRecordID, IdempotencyKey: idempotencyKey, Reason: request.Reason,
	})
	if err != nil {
		return CustomerCreateResponse{}, err
	}
	return toCustomerResponse(created), nil
}

// CreateOpportunity 在通道内创建商机。
// 参数：ctx 携带机器身份，actorIdentityID 为审计归属，idempotencyKey 为必需的幂等键。
// 返回值：创建或幂等重放的商机投影（固定为初步接触 + FOLLOWING）。
func (s *Service) CreateOpportunity(ctx context.Context, actorIdentityID, idempotencyKey string, request OpportunityCreateRequest) (OpportunityCreateResponse, error) {
	if err := s.available(); err != nil {
		return OpportunityCreateResponse{}, err
	}
	created, err := s.opportunities.CreateProvision(ctx, opportunity.ProvisionCreateRequest{
		Name: request.Name, CustomerID: request.CustomerID, Type: request.Type, Source: request.Source,
		ExpectedAmount: request.ExpectedAmount, ExpectedSignDate: request.ExpectedSignDate,
		RequirementSummary: request.RequirementSummary, SystemCount: request.SystemCount,
		PainPoints: request.PainPoints, CompetitorInfo: request.CompetitorInfo,
		OwnerIdentityID: request.OwnerIdentityID, ActorIdentityID: actorIdentityID,
		ExternalRecordID: request.ExternalRecordID, IdempotencyKey: idempotencyKey, Reason: request.Reason,
	})
	if err != nil {
		return OpportunityCreateResponse{}, err
	}
	return toOpportunityResponse(created), nil
}

// SignOpportunity 把商机推进到「已签约 + CLOSED」。
// 参数：ctx 携带机器身份，actorIdentityID 为审计归属，id 为商机标识，request 为合同引用与期望版本。
// 返回值：推进后（或同合同引用重复调用回放）的终态投影。
func (s *Service) SignOpportunity(ctx context.Context, actorIdentityID string, id uint64, request OpportunitySignRequest) (OpportunitySignResponse, error) {
	if err := s.available(); err != nil {
		return OpportunitySignResponse{}, err
	}
	if id == 0 {
		return OpportunitySignResponse{}, ErrInvalidRequest
	}
	signed, replayed, err := s.opportunities.SignProvision(ctx, id, opportunity.ProvisionSignRequest{
		ContractRef: request.ContractRef, ContractID: request.ContractID, ExpectedVersion: request.ExpectedVersion,
		Reason: request.Reason, ActorIdentityID: actorIdentityID, ExternalRecordID: request.ExternalRecordID,
	})
	if err != nil {
		return OpportunitySignResponse{}, err
	}
	if signed == nil {
		return OpportunitySignResponse{}, ErrUnavailable
	}
	return OpportunitySignResponse{
		Origin: Origin, ID: signed.ID, OpportunityNo: signed.OpportunityNo, CustomerID: signed.CustomerID, Name: signed.Name,
		CurrentStage: signed.CurrentStage, OppStatus: signed.Status, ContractRef: signed.ContractRef, Version: signed.Version,
		IdempotentReplay: replayed,
	}, nil
}

func toCustomerResponse(value *customer.Response) CustomerCreateResponse {
	result := CustomerCreateResponse{Origin: Origin}
	if value == nil {
		return result
	}
	contacts := make([]CustomerContactResponse, 0, len(value.Contacts))
	for _, contact := range value.Contacts {
		contacts = append(contacts, CustomerContactResponse{ID: contact.ID, Name: contact.Name, PhoneMasked: contact.PhoneMasked, EmailMasked: contact.EmailMasked, IsRegistration: contact.IsRegistration})
	}
	result.ID, result.CustomerNo, result.Name, result.Status = value.ID, value.CustomerNo, value.Name, value.Status
	result.CustomerType, result.Industry, result.Region = value.CustomerType, value.Industry, value.Region
	result.OwnerIdentityID, result.OwnerOrgID, result.Version = value.OwnerUserID, value.OwnerOrgID, value.Version
	result.Contacts, result.CreatedAt = contacts, value.CreatedAt
	return result
}

func toOpportunityResponse(value *opportunity.Response) OpportunityCreateResponse {
	result := OpportunityCreateResponse{Origin: Origin}
	if value == nil {
		return result
	}
	result.ID, result.OpportunityNo, result.Name, result.CustomerID = value.ID, value.OpportunityNo, value.Name, value.CustomerID
	result.Type, result.Source = value.Type, value.Source
	result.ExpectedAmount, result.ExpectedSignDate = value.ExpectedAmount, value.ExpectedSignDate
	result.RequirementSummary = value.RequirementSummary
	result.OwnerIdentityID, result.OwnerOrgID = value.OwnerUserID, value.OwnerOrgID
	result.CurrentStage, result.OppStatus, result.Version = value.CurrentStage, value.Status, value.Version
	return result
}
