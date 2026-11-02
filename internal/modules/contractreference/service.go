package contractreference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"gorm.io/gorm"
)

var ErrInvalidReference = apperror.New(http.StatusUnprocessableEntity, "CRM_CONTRACT_REFERENCE_INVALID", "customer or opportunity reference is invalid")

var ErrActorContextRequired = apperror.New(http.StatusUnprocessableEntity,
	"CRM_CONTRACT_REFERENCE_ACTOR_CONTEXT_REQUIRED", "请使用同一账号连接 CRM 后重试")

const maximumActorAuthorizationAge = 2 * time.Minute

type CustomerReference struct {
	ID     uint64 `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type OpportunityReference struct {
	ID         uint64 `json:"id"`
	Name       string `json:"name"`
	CustomerID uint64 `json:"customer_id"`
	Status     string `json:"status"`
}

type Reference struct {
	Customer    CustomerReference     `json:"customer"`
	Opportunity *OpportunityReference `json:"opportunity,omitempty"`
}

type Repository interface {
	ActorPrincipal(context.Context, string, string, time.Time) (auth.Principal, error)
	ActiveCustomer(context.Context, auth.Principal, uint64) (CustomerReference, error)
	UsableOpportunity(context.Context, auth.Principal, uint64, uint64) (OpportunityReference, error)
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }

func (repository *GORMRepository) ActorPrincipal(ctx context.Context, tenantID, actorID string, now time.Time) (auth.Principal, error) {
	var session struct {
		PlatformUserID      string
		RolesJSON           []byte
		PermissionsJSON     []byte
		OrganizationIDsJSON []byte
		DataScopesJSON      []byte
	}
	err := repository.db.WithContext(ctx).Table("crm_oidc_sessions").
		Select("platform_user_id", "roles_json", "permissions_json", "organization_ids_json", "data_scopes_json").
		Where("tenant_id = ? AND platform_user_id = ? AND revoked_at IS NULL AND expires_at > ? AND authorization_checked_at >= ?", tenantID, actorID, now, now.Add(-maximumActorAuthorizationAge)).
		Order("authorization_checked_at DESC").Take(&session).Error
	if err != nil {
		return auth.Principal{}, err
	}
	var roles, permissions, organizationIDs []string
	var scopes []auth.DataScope
	if json.Unmarshal(session.RolesJSON, &roles) != nil || json.Unmarshal(session.PermissionsJSON, &permissions) != nil || json.Unmarshal(session.OrganizationIDsJSON, &organizationIDs) != nil || json.Unmarshal(session.DataScopesJSON, &scopes) != nil {
		return auth.Principal{}, errors.New("decode actor authorization snapshot")
	}
	permissionSet := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		permissionSet[permission] = struct{}{}
	}
	return auth.Principal{UserID: session.PlatformUserID, TenantID: tenantID, Roles: roles, Permissions: permissionSet, OrganizationIDs: organizationIDs, DataScopes: scopes, ScopeMode: scopeMode(scopes)}, nil
}

func (repository *GORMRepository) ActiveCustomer(ctx context.Context, principal auth.Principal, id uint64) (CustomerReference, error) {
	var result CustomerReference
	err := activeCustomerQuery(repository.db.WithContext(ctx), principal, id).Take(&result).Error
	return result, err
}

// activeCustomerQuery 构造“当前主体可见且仍可引用的客户”查询。
// 客户可见性必须与客户模块同口径：范围谓词直接复用 customer.ApplyDataScope（客户数据范围的
// 唯一权威实现），不在合同引用内复制。销售只看“当前负责人”owner_user_id；交接后原创建人
// 立即失去引用权限，created_by 仅作创建历史，不能作为访问依据。
// 参数：db 为未绑定的 GORM 连接；principal 为认证边界输出的主体；id 为待引用的客户ID。
// 返回值：已带范围、租户、状态与合并标记约束的查询。
func activeCustomerQuery(db *gorm.DB, principal auth.Principal, id uint64) *gorm.DB {
	return customer.ApplyDataScope(db.Model(&customer.Customer{}), principal).
		Select("id", "name", "status").
		Where("crm_customers.id = ? AND crm_customers.status = ? AND crm_customers.merged_into_id IS NULL", id, customer.StatusActive)
}

func (repository *GORMRepository) UsableOpportunity(ctx context.Context, principal auth.Principal, customerID, id uint64) (OpportunityReference, error) {
	var result OpportunityReference
	err := usableOpportunityQuery(repository.db.WithContext(ctx), principal, customerID, id).Take(&result).Error
	return result, err
}

// usableOpportunityQuery 构造“当前主体可见且未作废、属于指定客户”的商机查询。
// 商机范围与 opportunity 模块的数据范围谓词（repository.go scoped）逐字同口径：
// ALL 按租户、ORG 按 owner_org_id（组织集合为空即拒绝）、其余按当前负责人 owner_user_id。
// 商机模块没有“纯销售强制自管”的特例，这里也不额外收紧或放宽。
func usableOpportunityQuery(db *gorm.DB, principal auth.Principal, customerID, id uint64) *gorm.DB {
	query := db.Model(&opportunity.Opportunity{}).
		Select("id", "name", "customer_id", "opp_status AS status").
		Where("crm_opportunities.tenant_id = ? AND crm_opportunities.customer_id = ? AND crm_opportunities.id = ? AND crm_opportunities.deleted_at IS NULL AND crm_opportunities.opp_status <> ?", principal.TenantID, customerID, id, opportunity.StatusVoid)
	return applyScope(query, principal, "crm_opportunities.owner_user_id", "crm_opportunities.owner_org_id")
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

// Resolve 返回合同管理所需的最小权威 CRM 快照。
// 参数：ctx提供已验证机器租户，客户/商机ID及actorID限定当前操作者的数据范围。
// 返回值：最小引用；缺少近期授权、无效引用及依赖错误分别返回，不披露不可见客户。
func (service *Service) Resolve(ctx context.Context, customerID uint64, opportunityID *uint64, actorID string) (Reference, error) {
	machine, ok := auth.FromContext(ctx)
	if !ok || machine.TenantID == "" {
		return Reference{}, apperror.ErrUnauthenticated
	}
	if service == nil || service.repository == nil || customerID == 0 || actorID == "" {
		return Reference{}, ErrInvalidReference
	}
	actor, err := service.repository.ActorPrincipal(ctx, machine.TenantID, actorID, time.Now().UTC())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Reference{}, ErrActorContextRequired
	}
	if err != nil {
		return Reference{}, err
	}
	if !actor.HasPermission("customer.read") || opportunityID != nil && !actor.HasPermission("opportunity.read") {
		return Reference{}, ErrInvalidReference
	}
	customerReference, err := service.repository.ActiveCustomer(ctx, actor, customerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Reference{}, ErrInvalidReference
		}
		return Reference{}, err
	}
	result := Reference{Customer: customerReference}
	if opportunityID == nil {
		return result, nil
	}
	if *opportunityID == 0 {
		return Reference{}, ErrInvalidReference
	}
	opportunityReference, err := service.repository.UsableOpportunity(ctx, actor, customerID, *opportunityID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Reference{}, ErrInvalidReference
		}
		return Reference{}, err
	}
	result.Opportunity = &opportunityReference
	return result, nil
}

func scopeMode(scopes []auth.DataScope) auth.ScopeMode {
	for _, scope := range scopes {
		switch scope.ScopeType {
		case sharedauthorization.ScopeApplication, sharedauthorization.ScopeEnvironment, sharedauthorization.ScopeTenant:
			return auth.ScopeAll
		}
	}
	for _, scope := range scopes {
		if scope.ScopeType == sharedauthorization.ScopeOrg {
			return auth.ScopeOrg
		}
	}
	return auth.ScopeSelf
}

// applyScope 只用于商机侧，逐字对应 opportunity 模块的数据范围谓词（scoped），
// 客户侧不再使用本函数：客户范围必须复用 customer.ApplyDataScope。
func applyScope(query *gorm.DB, principal auth.Principal, ownerUserColumn, ownerOrgColumn string) *gorm.DB {
	switch principal.ScopeMode {
	case auth.ScopeAll:
		return query
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return query.Where("1 = 0")
		}
		return query.Where(ownerOrgColumn+" IN ?", principal.OrganizationIDs)
	default:
		return query.Where(ownerUserColumn+" = ?", principal.UserID)
	}
}
