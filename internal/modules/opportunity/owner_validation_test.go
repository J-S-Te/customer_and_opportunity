package opportunity

import (
	"context"
	"errors"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type ownerCatalogProbe struct {
	err             error
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
	return nil, probe.err
}

// OWNER_DIRECTORY_ENABLED=false（目录适配器未接线）时改派必须失败关闭：
// 没有权威目录就无法证明目标用户/组织是有效任职关系，不能把任意字符串写成负责人。
func TestChangeOwnerWithoutOwnerDirectoryFailsClosed(t *testing.T) {
	repository := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	service := createTestService(repository, &countingAuditWriter{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	change := ChangeOwnerRequest{OwnerUserID: "attacker", OwnerOrgID: "org-attacker", Version: 1, Reason: "调整负责人", IdempotencyKey: "owner-change-key"}
	if result, err := service.ChangeOwner(ctx, 17, change); result != nil || !errors.Is(err, ownerdirectory.ErrUnavailable) {
		t.Fatalf("change owner result=%#v err=%v, want %v", result, err, ownerdirectory.ErrUnavailable)
	}
	if repository.findCalls != 0 {
		t.Fatalf("rejected owner change reached idempotency persistence: find calls=%d", repository.findCalls)
	}
	// 创建路径不受影响：负责人强制继承认证主体，不依赖目录改派校验。
	if _, err := service.Create(ctx, createTestInput()); errors.Is(err, ownerdirectory.ErrUnavailable) {
		t.Fatalf("create path must not fail on the missing owner directory: %v", err)
	}
}

func TestOpportunityCreateAndOwnerChangeFailClosedBeforePersistence(t *testing.T) {
	directoryErr := errors.New("authoritative owner directory unavailable")
	probe := &ownerCatalogProbe{err: directoryErr}
	repository := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	service := createTestService(repository, &countingAuditWriter{}).UseOwnerDirectory(probe)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a", PrimaryOrgID: "org-a", OrganizationIDs: []string{"org-a"}})

	if result, err := service.Create(ctx, createTestInput()); result != nil || !errors.Is(err, directoryErr) {
		t.Fatalf("create result=%#v err=%v", result, err)
	}
	if probe.userID != "actor-a" || probe.organizationID != "org-a" {
		t.Fatalf("create pair=(%q,%q)", probe.userID, probe.organizationID)
	}
	if repository.visibilityChecks != 0 || repository.findCalls != 0 || repository.created != 0 {
		t.Fatalf("rejected create reached persistence: %#v", repository)
	}

	change := ChangeOwnerRequest{OwnerUserID: " owner-b ", OwnerOrgID: " org-b ", Version: 1, Reason: " 调整负责人 ", IdempotencyKey: " owner-change-key "}
	if result, err := service.ChangeOwner(ctx, 17, change); result != nil || !errors.Is(err, directoryErr) {
		t.Fatalf("change owner result=%#v err=%v", result, err)
	}
	if probe.validationCalls != 2 || probe.userID != "owner-b" || probe.organizationID != "org-b" {
		t.Fatalf("validation calls=%d pair=(%q,%q)", probe.validationCalls, probe.userID, probe.organizationID)
	}
	if repository.findCalls != 0 {
		t.Fatalf("rejected owner change reached idempotency persistence: find calls=%d", repository.findCalls)
	}
}
