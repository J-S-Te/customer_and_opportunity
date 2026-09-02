package customer

import (
	"context"
	"errors"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type ownerCatalogProbe struct {
	err             error
	users           map[string]ownerdirectory.User
	userID          string
	organizationID  string
	validationCalls int
}

func (probe *ownerCatalogProbe) List(context.Context, ownerdirectory.Query) (ownerdirectory.Page, error) {
	return ownerdirectory.Page{}, probe.err
}

func (probe *ownerCatalogProbe) Validate(_ context.Context, userID, organizationID string) error {
	probe.validationCalls++
	probe.userID, probe.organizationID = userID, organizationID
	return probe.err
}

func (probe *ownerCatalogProbe) Resolve(context.Context, []string) (map[string]ownerdirectory.User, error) {
	return probe.users, probe.err
}

func TestOwnerDisplayNamesAreEnrichedWithoutBlockingCustomerReads(t *testing.T) {
	probe := &ownerCatalogProbe{users: map[string]ownerdirectory.User{
		"owner-a": {ID: "owner-a", DisplayName: "张六"},
	}}
	service := (&Service{}).UseOwnerDirectory(probe)
	responses := service.withOwnerDisplayNames(context.Background(), []Response{{OwnerUserID: "owner-a"}, {OwnerUserID: "missing-owner"}})
	if responses[0].OwnerDisplayName != "张六" || responses[1].OwnerDisplayName != "" {
		t.Fatalf("unexpected owner names: %#v", responses)
	}

	probe.err = errors.New("directory temporarily unavailable")
	responses = service.withOwnerDisplayNames(context.Background(), []Response{{OwnerUserID: "owner-a"}})
	if responses[0].OwnerDisplayName != "" {
		t.Fatalf("directory failure must not inject stale owner data: %#v", responses[0])
	}
}

func TestCustomerWritesFailClosedBeforePersistenceWhenOwnerDirectoryRejectsSelection(t *testing.T) {
	directoryErr := errors.New("authoritative owner directory unavailable")
	probe := &ownerCatalogProbe{err: directoryErr}
	repository := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repository, &createAuditStub{}).UseOwnerDirectory(probe)
	ctx := auth.WithPrincipal(context.Background(), createTestPrincipal("tenant-a", "actor-a"))

	if result, err := service.Create(ctx, createTestRequest("owner-validation")); result != nil || !errors.Is(err, directoryErr) {
		t.Fatalf("create result=%#v err=%v", result, err)
	}
	if probe.userID != "actor-a" || probe.organizationID != "org-a" {
		t.Fatalf("create pair=(%q,%q)", probe.userID, probe.organizationID)
	}
	if repository.findReplayCalls != 0 || repository.nextNumberCalls != 0 || repository.createCalls != 0 {
		t.Fatalf("rejected owner selection reached persistence: %#v", repository)
	}

	request := UpdateRequest{OwnerUserID: " owner-b ", OwnerOrgID: " org-b ", Contacts: []UpdateContactInput{{Name: "联系人", Phone: stringPointer("13800138000"), IsRegistration: true}}}
	if result, err := service.Update(ctx, 71, request); result != nil || !errors.Is(err, directoryErr) {
		t.Fatalf("update result=%#v err=%v", result, err)
	}
	if probe.validationCalls != 2 || probe.userID != "owner-b" || probe.organizationID != "org-b" {
		t.Fatalf("validation calls=%d pair=(%q,%q)", probe.validationCalls, probe.userID, probe.organizationID)
	}
}
