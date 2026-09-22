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
	query := repository.db.WithContext(ctx).Model(&customer.Customer{}).
		Select("id", "name", "status").
		Where("tenant_id = ? AND id = ? AND deleted_at IS NULL AND status = ? AND merged_into_id IS NULL", principal.TenantID, id, customer.StatusActive)
	if salesOnly(principal) {
		query = query.Where("created_by = ?", principal.UserID)
	} else {
		query = applyScope(query, principal, "owner_user_id", "owner_org_id")
	}
	err := query.Take(&result).Error
	return result, err
}

func (repository *GORMRepository) UsableOpportunity(ctx context.Context, principal auth.Principal, customerID, id uint64) (OpportunityReference, error) {
	var result OpportunityReference
	query := repository.db.WithContext(ctx).Model(&opportunity.Opportunity{}).
		Select("id", "name", "customer_id", "opp_status AS status").
		Where("tenant_id = ? AND customer_id = ? AND id = ? AND deleted_at IS NULL AND opp_status <> ?", principal.TenantID, customerID, id, opportunity.StatusVoid)
	query = applyScope(query, principal, "owner_user_id", "owner_org_id")
	err := query.Take(&result).Error
	return result, err
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

// Resolve returns the smallest authoritative CRM snapshot required by Contract
// Management. The tenant is always taken from the verified machine principal;
// caller-supplied tenant or customer names are deliberately not accepted.
func (service *Service) Resolve(ctx context.Context, customerID uint64, opportunityID *uint64, actorID string) (Reference, error) {
	machine, ok := auth.FromContext(ctx)
	if !ok || machine.TenantID == "" {
		return Reference{}, apperror.ErrUnauthenticated
	}
	if service == nil || service.repository == nil || customerID == 0 || actorID == "" {
		return Reference{}, ErrInvalidReference
	}
	actor, err := service.repository.ActorPrincipal(ctx, machine.TenantID, actorID, time.Now().UTC())
	if err != nil || !actor.HasPermission("customer.read") || opportunityID != nil && !actor.HasPermission("opportunity.read") {
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

func salesOnly(principal auth.Principal) bool {
	hasSales := false
	for _, role := range principal.Roles {
		if role == "sales" {
			hasSales = true
		}
		switch role {
		case "sales_director", "customer_admin", "crm_super_admin", "auditor", "admin":
			return false
		}
	}
	return hasSales
}

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
