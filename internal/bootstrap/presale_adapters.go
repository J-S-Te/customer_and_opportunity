package bootstrap

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
)

// 售前模块通过商机服务边界读取快照，有意不直接查询商机表，从而复用租户、数据范围和可见性校验。
type presaleOpportunityReader struct {
	service *opportunity.Service
}

func (a presaleOpportunityReader) GetAccessible(ctx context.Context, actor presale.Actor, opportunityID uint64) (presale.OpportunitySnapshot, error) {
	permissions := make(map[string]struct{}, len(actor.Permissions))
	for code, allowed := range actor.Permissions {
		if allowed {
			permissions[code] = struct{}{}
		}
	}
	principal := auth.Principal{
		UserID: actor.UserID, PersonID: actor.PersonID, TenantID: actor.TenantID,
		DisplayName: actor.UserName, Permissions: permissions,
	}
	switch {
	case actor.Roles["technical_lead"] || actor.Roles["team_lead"] || actor.Roles["sales_director"]:
		principal.ScopeMode = auth.ScopeAll
	default:
		principal.ScopeMode = auth.ScopeSelf
	}
	value, err := a.service.Get(auth.WithPrincipal(ctx, principal), opportunityID)
	if err != nil {
		return presale.OpportunitySnapshot{}, err
	}
	return presale.OpportunitySnapshot{ID: value.ID, OpportunityNo: value.OpportunityNo}, nil
}

type presalePhoneProtector struct {
	codec *security.SensitiveCodec
}

func (p presalePhoneProtector) Encrypt(_ context.Context, plaintext string) ([]byte, error) {
	return p.codec.Encrypt(plaintext)
}

func (p presalePhoneProtector) Decrypt(_ context.Context, ciphertext []byte) (string, error) {
	return p.codec.Decrypt(ciphertext)
}

func (presalePhoneProtector) Mask(plaintext string) string {
	return security.MaskPhone(plaintext)
}

type presaleActorResolver struct{}

func (presaleActorResolver) Resolve(ctx context.Context) (presale.Actor, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok {
		return presale.Actor{}, apperror.ErrUnauthenticated
	}
	roles := make(map[string]bool, len(principal.Roles))
	for _, role := range principal.Roles {
		roles[role] = true
	}
	permissions := make(map[string]bool, len(principal.Permissions))
	for code := range principal.Permissions {
		permissions[code] = true
	}
	return presale.Actor{
		TenantID: principal.TenantID, UserID: principal.UserID, UserName: principal.DisplayName,
		PersonID: principal.PersonID, ScopeMode: string(principal.ScopeMode), OrganizationIDs: append([]string(nil), principal.OrganizationIDs...),
		Roles: roles, Permissions: permissions, RequestID: request.ID(ctx),
	}, nil
}

type requestIDGenerator struct{}

func (requestIDGenerator) NewID() string { return request.NewID() }
