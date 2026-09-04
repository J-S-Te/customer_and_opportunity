package bootstrap

import (
	"context"
	"errors"

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
	principal := presaleOpportunityPrincipal(actor)
	value, err := a.service.Get(auth.WithPrincipal(ctx, principal), opportunityID)
	if err != nil {
		return presale.OpportunitySnapshot{}, err
	}
	return presale.OpportunitySnapshot{ID: value.ID, OpportunityNo: value.OpportunityNo}, nil
}

func (a presaleOpportunityReader) EnsurePresaleEligible(ctx context.Context, actor presale.Actor, opportunityID uint64) error {
	err := a.service.EnsurePresaleEligible(auth.WithPrincipal(ctx, presaleOpportunityPrincipal(actor)), opportunityID)
	if errors.Is(err, opportunity.ErrPresaleIneligible) {
		return presale.ErrOpportunityNotEligible
	}
	return err
}

// presaleOpportunityPrincipal 复用基础平台已经签发并由 CRM 会话保存的数据范围。
// 商机选择列表和售前创建校验必须使用同一份 SELF/ORG/ALL 上下文，不能根据角色名
// 再次推断范围，否则 ORG 用户会在列表中选到商机后被创建接口错误拒绝。
func presaleOpportunityPrincipal(actor presale.Actor) auth.Principal {
	permissions := make(map[string]struct{}, len(actor.Permissions))
	for code, allowed := range actor.Permissions {
		if allowed {
			permissions[code] = struct{}{}
		}
	}
	scopeMode := auth.ScopeMode(actor.ScopeMode)
	switch scopeMode {
	case auth.ScopeSelf, auth.ScopeOrg, auth.ScopeAll:
	default:
		// 缺失或未知范围保持最小权限，绝不通过角色名称扩大数据访问范围。
		scopeMode = auth.ScopeSelf
	}
	return auth.Principal{
		UserID: actor.UserID, PersonID: actor.PersonID, TenantID: actor.TenantID,
		DisplayName: actor.UserName, Permissions: permissions, ScopeMode: scopeMode,
		OrganizationIDs: append([]string(nil), actor.OrganizationIDs...),
	}
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
		// 售前执行人来自基础平台授权用户目录，与申请人使用同一 user_id 命名空间。
		// 不再依赖 OIDC 中可选的外部 PMS person_id，避免被指派用户无法读取任务或登记工时。
		PersonID: principal.UserID, ScopeMode: string(principal.ScopeMode), OrganizationIDs: append([]string(nil), principal.OrganizationIDs...),
		Roles: roles, Permissions: permissions, RequestID: request.ID(ctx),
	}, nil
}

type requestIDGenerator struct{}

func (requestIDGenerator) NewID() string { return request.NewID() }
