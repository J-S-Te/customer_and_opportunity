package crmauth

import (
	"errors"
	"testing"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// TestImportProvisionScopesAreWhitelisted 验证历史合同导入开立通道的两个专用 scope 被机器白名单接受。
// 参数：t 为当前测试上下文。
// 返回值：无；白名单缺项时测试失败（缺项会让合同侧在验签层直接 401）。
func TestImportProvisionScopesAreWhitelisted(t *testing.T) {
	claims := machineClaims{Subject: "contract-production", OAuthClientID: "01J-OAUTH-ROW", TenantID: "tenant-1", TokenUse: "application",
		Scopes: []string{"customer.import_provision", "opportunity.import_provision"}}
	if err := validateMachineClaims(claims, "tenant-1"); err != nil {
		t.Fatalf("历史导入开立 scope 被拒绝: %v", err)
	}
	for _, scope := range claims.Scopes {
		if !validMachinePermission(scope) {
			t.Fatalf("scope %q 未加入机器白名单", scope)
		}
	}
}

// TestImportProvisionScopesDoNotGrantOrdinaryCreate 验证通道 scope 不等于普通创建权限。
// 参数：t 为当前测试上下文。
// 返回值：无；白名单出现 customer.create/opportunity.create 时测试失败，
// 因为那会让机器凭据获得普通创建接口的写入能力。
func TestImportProvisionScopesDoNotGrantOrdinaryCreate(t *testing.T) {
	for _, scope := range []string{"customer.create", "opportunity.create", "customer.import_provision.write", "opportunity.import_provision.sign"} {
		if validMachinePermission(scope) {
			t.Fatalf("普通创建或近似 scope %q 不得进入机器白名单", scope)
		}
	}
	// 只携带通道 scope 的机器主体不应通过普通创建接口的权限判断。
	principal := sharedauth.Principal{UserID: "machine:contract-production", TenantID: "tenant-1",
		Permissions: map[string]struct{}{"customer.import_provision": {}, "opportunity.import_provision": {}}}
	for _, permission := range []string{"customer.create", "opportunity.create", "opportunity.stage.change"} {
		if principal.HasPermission(permission) {
			t.Fatalf("通道凭据仍持有 %q", permission)
		}
	}
	if err := validateMachineClaims(machineClaims{Subject: "contract-production", OAuthClientID: "01J-OAUTH-ROW", TenantID: "tenant-1",
		TokenUse: "application", Scopes: []string{"customer.create"}}, "tenant-1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("customer.create 作为机器 scope 应被拒绝: %v", err)
	}
}
