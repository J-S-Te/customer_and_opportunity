package contractreference

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
)

type repositoryStub struct {
	customer    CustomerReference
	opportunity OpportunityReference
	customerErr error
	oppErr      error
	tenant      string
	customerID  uint64
}

func (stub *repositoryStub) ActiveCustomer(_ context.Context, tenant auth.Principal, customerID uint64) (CustomerReference, error) {
	stub.tenant, stub.customerID = tenant.TenantID, customerID
	return stub.customer, stub.customerErr
}
func (stub *repositoryStub) UsableOpportunity(_ context.Context, tenant auth.Principal, customerID, _ uint64) (OpportunityReference, error) {
	stub.tenant, stub.customerID = tenant.TenantID, customerID
	return stub.opportunity, stub.oppErr
}

func (stub *repositoryStub) ActorPrincipal(_ context.Context, tenant, actor string, _ time.Time) (auth.Principal, error) {
	return auth.Principal{TenantID: tenant, UserID: actor, Permissions: map[string]struct{}{"customer.read": {}, "opportunity.read": {}}, ScopeMode: auth.ScopeAll}, nil
}

func TestResolveUsesVerifiedTenantAndChecksOpportunityCustomer(t *testing.T) {
	repository := &repositoryStub{
		customer:    CustomerReference{ID: 7, Name: "权威客户", Status: "ACTIVE"},
		opportunity: OpportunityReference{ID: 9, Name: "权威商机", CustomerID: 7, Status: "FOLLOWING"},
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a"})
	opportunityID := uint64(9)
	result, err := NewService(repository).Resolve(ctx, 7, &opportunityID, "actor-1")
	if err != nil {
		t.Fatal(err)
	}
	if repository.tenant != "tenant-a" || repository.customerID != 7 || result.Customer.Name != "权威客户" || result.Opportunity == nil || result.Opportunity.Name != "权威商机" {
		t.Fatalf("unexpected result %#v repository=%#v", result, repository)
	}
}

func TestResolveMasksMissingOrCrossCustomerReferences(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a"})
	for _, repository := range []*repositoryStub{
		{customerErr: gorm.ErrRecordNotFound},
		{customer: CustomerReference{ID: 7}, oppErr: gorm.ErrRecordNotFound},
	} {
		opportunityID := uint64(9)
		_, err := NewService(repository).Resolve(ctx, 7, &opportunityID, "actor-1")
		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("Resolve() error=%v, want masked invalid reference", err)
		}
	}
}
