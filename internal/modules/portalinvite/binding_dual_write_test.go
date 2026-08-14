package portalinvite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeBindingWriter struct {
	platformUserIDs []string
	refs            []string
	keys            []string
	err             error
}

func (writer *fakeBindingWriter) BindCustomerIdempotent(_ context.Context, platformUserID, customerRef, key string) error {
	writer.platformUserIDs = append(writer.platformUserIDs, platformUserID)
	writer.refs = append(writer.refs, customerRef)
	writer.keys = append(writer.keys, key)
	return writer.err
}

type fakeBindingDisabler struct {
	platformUserIDs []string
	refs            []string
	keys            []string
	err             error
}

func (disabler *fakeBindingDisabler) DisableCustomerBindingIdempotent(_ context.Context, platformUserID, customerRef, key string) error {
	disabler.platformUserIDs = append(disabler.platformUserIDs, platformUserID)
	disabler.refs = append(disabler.refs, customerRef)
	disabler.keys = append(disabler.keys, key)
	return disabler.err
}

// Phase 2 双写：绑定失败不中断邀请开通，门户映射仍被调用且主流程成功。
func TestCreateDualWriteBindingFailureDoesNotBlockInvite(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	repo := &fakeRepo{}
	writer := &fakeAudit{}
	random := &deterministicRandom{}
	identity := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000", PhoneMasked: "138****8000"}
	portal := &fakePortal{mapping: PortalMapping{PortalAccountID: "42"}}
	binding := &fakeBindingWriter{err: errors.New("platform binding endpoint down")}
	service := NewService(repo, fakeCustomer{identity: identity}, &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "platform-subject-123456789", AccountNo: "PA-1"}}, portal, writer, []byte(strings.Repeat("p", 32)), "https://portal.example/customer-portal", fixedClock{now}, random, testOperationProtector{}, WithPlatformBindingWriter(binding))

	if _, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-1"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if portal.calls != 1 {
		t.Fatalf("portal mapping calls = %d, want 1 (dual-write must not block)", portal.calls)
	}
	if len(binding.keys) != 1 || binding.platformUserIDs[0] != "platform-subject-123456789" || binding.refs[0] != "7" || !strings.HasSuffix(binding.keys[0], "B") {
		t.Fatalf("binding call = ids:%v refs:%v keys:%v", binding.platformUserIDs, binding.refs, binding.keys)
	}
	pending := 0
	for _, event := range writer.events {
		if event.Operation == "PLATFORM_BINDING" && event.Result == "PENDING" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("PLATFORM_BINDING PENDING audit events = %d, want 1", pending)
	}
}

// 双写成功路径：绑定与门户映射都调用，审计 SUCCESS。
func TestCreateDualWriteBindingSuccess(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	repo := &fakeRepo{}
	writer := &fakeAudit{}
	identity := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000", PhoneMasked: "138****8000"}
	portal := &fakePortal{mapping: PortalMapping{PortalAccountID: "42"}}
	binding := &fakeBindingWriter{}
	service := NewService(repo, fakeCustomer{identity: identity}, &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "platform-subject-123456789", AccountNo: "PA-1"}}, portal, writer, []byte(strings.Repeat("p", 32)), "https://portal.example/customer-portal", fixedClock{now}, &deterministicRandom{}, testOperationProtector{}, WithPlatformBindingWriter(binding))

	if _, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-1"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	success := 0
	for _, event := range writer.events {
		if event.Operation == "PLATFORM_BINDING" && event.Result == "SUCCESS" {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("PLATFORM_BINDING SUCCESS audit events = %d, want 1", success)
	}
}

// Phase 5 单写：跳过门户映射调用，门户账号标识本地合成；平台绑定失败即失败关闭。
func TestCreatePlatformOnlySkipsPortalMapping(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	repo := &fakeRepo{}
	writer := &fakeAudit{}
	identity := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000", PhoneMasked: "138****8000"}
	portal := &fakePortal{mapping: PortalMapping{PortalAccountID: "42"}}
	binding := &fakeBindingWriter{}
	service := NewService(repo, fakeCustomer{identity: identity}, &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "platform-subject-123456789", AccountNo: "PA-1"}}, portal, writer, []byte(strings.Repeat("p", 32)), "https://portal.example/customer-portal", fixedClock{now}, &deterministicRandom{}, testOperationProtector{}, WithPlatformBindingWriter(binding), WithPlatformBindingOnly())

	result, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if portal.calls != 0 {
		t.Fatalf("portal mapping calls = %d, want 0 in platform-only mode", portal.calls)
	}
	if result.InviteNo == "" || len(binding.keys) != 1 {
		t.Fatalf("result = %#v binding keys = %v", result, binding.keys)
	}
}

func TestCreatePlatformOnlyFailsClosedWhenBindingFails(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	repo := &fakeRepo{}
	writer := &fakeAudit{}
	identity := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000", PhoneMasked: "138****8000"}
	portal := &fakePortal{mapping: PortalMapping{PortalAccountID: "42"}}
	binding := &fakeBindingWriter{err: errors.New("platform binding endpoint down")}
	service := NewService(repo, fakeCustomer{identity: identity}, &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "platform-subject-123456789", AccountNo: "PA-1"}}, portal, writer, []byte(strings.Repeat("p", 32)), "https://portal.example/customer-portal", fixedClock{now}, &deterministicRandom{}, testOperationProtector{}, WithPlatformBindingWriter(binding), WithPlatformBindingOnly())

	if _, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-1"}); err == nil {
		t.Fatal("Create() succeeded although the platform binding failed in platform-only mode")
	}
	if portal.calls != 0 {
		t.Fatalf("portal mapping calls = %d, want 0", portal.calls)
	}
	if len(repo.compensations) != 1 || repo.compensations[0].TaskType != CompensationBinding {
		t.Fatalf("compensations = %#v", repo.compensations)
	}
}

// 双写关闭（默认）：不注入绑定适配器时行为与旧版一致，不发生平台绑定调用。
func TestCreateWithoutBindingAdapterKeepsLegacyBehavior(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	repo := &fakeRepo{}
	writer := &fakeAudit{}
	identity := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000", PhoneMasked: "138****8000"}
	service := NewService(repo, fakeCustomer{identity: identity}, &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "platform-subject-123456789", AccountNo: "PA-1"}}, &fakePortal{mapping: PortalMapping{PortalAccountID: "42"}}, writer, []byte(strings.Repeat("p", 32)), "https://portal.example/customer-portal", fixedClock{now}, &deterministicRandom{}, testOperationProtector{})

	if _, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-1"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, event := range writer.events {
		if event.Operation == "PLATFORM_BINDING" {
			t.Fatal("PLATFORM_BINDING audit written without a binding adapter")
		}
	}
}
