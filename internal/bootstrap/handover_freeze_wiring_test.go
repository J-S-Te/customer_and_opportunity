package bootstrap

import (
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

type handoverFreezeSourceStub struct{}

func (handoverFreezeSourceStub) ActiveFreezeUser(context.Context, string, string) (bool, error) {
	return false, nil
}

// 生产装配必须把交接冻结强制接到客户、商机、售前三条责任写入路径。
// 冻结检查是 fail-closed，漏接任何一条都会让该模块的全部责任写入被拒绝，
// 因此这里断言装配函数本身能在组装期发现漏接。
func TestWireHandoverFreezeEnforcementCoversAllResponsibilityWriters(t *testing.T) {
	customerService := customer.NewService(nil, nil, nil, nil)
	opportunityService := opportunity.NewService(nil, nil, nil, nil)
	presaleService := presale.NewService(nil, nil, nil, presale.SystemClock{}, nil)

	if customerService.HandoverFreezeEnforced() || opportunityService.HandoverFreezeEnforced() || presaleService.HandoverFreezeEnforced() {
		t.Fatal("freshly built services must not report freeze enforcement before wiring")
	}
	if err := wireHandoverFreezeEnforcement(customerService, opportunityService, presaleService, handoverFreezeSourceStub{}); err != nil {
		t.Fatalf("wiring error = %v", err)
	}
	for name, enforced := range map[string]bool{
		"customer":    customerService.HandoverFreezeEnforced(),
		"opportunity": opportunityService.HandoverFreezeEnforced(),
		"presale":     presaleService.HandoverFreezeEnforced(),
	} {
		if !enforced {
			t.Fatalf("%s freeze enforcement is not wired", name)
		}
	}
}

// 缺少任一写入方或冻结源时装配必须直接失败，而不是留到运行期变成 500。
func TestWireHandoverFreezeEnforcementRejectsIncompleteAssembly(t *testing.T) {
	customerService := customer.NewService(nil, nil, nil, nil)
	opportunityService := opportunity.NewService(nil, nil, nil, nil)
	presaleService := presale.NewService(nil, nil, nil, presale.SystemClock{}, nil)

	if err := wireHandoverFreezeEnforcement(nil, opportunityService, presaleService, handoverFreezeSourceStub{}); err == nil {
		t.Fatal("missing customer service must fail the assembly")
	}
	if err := wireHandoverFreezeEnforcement(customerService, nil, presaleService, handoverFreezeSourceStub{}); err == nil {
		t.Fatal("missing opportunity service must fail the assembly")
	}
	if err := wireHandoverFreezeEnforcement(customerService, opportunityService, nil, handoverFreezeSourceStub{}); err == nil {
		t.Fatal("missing presale service must fail the assembly")
	}
	if err := wireHandoverFreezeEnforcement(customerService, opportunityService, presaleService, nil); err == nil {
		t.Fatal("missing freeze source must fail the assembly")
	}
}
