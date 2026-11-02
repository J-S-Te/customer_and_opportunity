package handover

import (
	"context"
	"errors"
	"testing"
	"time"
)

type repositoryFake struct {
	instances        []ApprovalInstance
	freezes          map[string]Freeze
	responsibilities []Responsibility
	receipts         map[string]Receipt
	transferCalls    int
	released         []string
}

func newRepositoryFake(resources ...string) *repositoryFake {
	fake := &repositoryFake{freezes: map[string]Freeze{}, receipts: map[string]Receipt{}}
	for _, resource := range resources {
		fake.responsibilities = append(fake.responsibilities, Responsibility{
			ResourceType: ResourceCustomer, ResourceID: resource, ResourceName: resource,
			OwnerUserID: "user-leaver", Category: CategoryCustomerOwner, Version: 3, UpdatedAt: time.Now().UTC(),
		})
	}
	return fake
}

func (fake *repositoryFake) CreateFreeze(_ context.Context, freeze Freeze) (Freeze, bool, error) {
	key := freeze.TenantID + "|" + freeze.BatchID + "|" + freeze.UserID
	if existing, ok := fake.freezes[key]; ok {
		return existing, false, nil
	}
	fake.freezes[key] = freeze
	return freeze, true, nil
}

func (fake *repositoryFake) ReleaseFreeze(_ context.Context, tenantID, batchID, outcome string, now time.Time) error {
	fake.released = append(fake.released, tenantID+"|"+batchID+"|"+outcome)
	for key, freeze := range fake.freezes {
		if freeze.BatchID == batchID {
			freeze.Status, freeze.ReleasedAt, freeze.Outcome = FreezeReleased, &now, outcome
			fake.freezes[key] = freeze
		}
	}
	return nil
}

func (fake *repositoryFake) ActiveFreeze(_ context.Context, tenantID, userID string) (Freeze, bool, error) {
	for _, freeze := range fake.freezes {
		if freeze.TenantID == tenantID && freeze.UserID == userID && freeze.Status == FreezeFrozen {
			return freeze, true, nil
		}
	}
	return Freeze{}, false, nil
}

func (fake *repositoryFake) ListResponsibilities(_ context.Context, _, userID, cursor string, limit int) ([]Responsibility, error) {
	items := []Responsibility{}
	for _, item := range fake.responsibilities {
		if item.OwnerUserID != userID || (cursor != "" && item.ResourceID <= cursor) {
			continue
		}
		items = append(items, item)
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

func (fake *repositoryFake) TransferOwnership(_ context.Context, input TransferInput, now time.Time) (Receipt, error) {
	fake.transferCalls++
	receipt := Receipt{ItemID: input.ItemID, IdempotencyKey: input.IdempotencyKey, Status: ReceiptApplied,
		AppliedVersion: input.ExpectedVersion + 1, EvidenceRef: "audit:" + input.ResourceID, ReceivedAt: now}
	fake.receipts[input.TenantID+"|"+input.IdempotencyKey] = receipt
	return receipt, nil
}

func (fake *repositoryFake) ListPendingApprovalInstances(context.Context, string, int) ([]ApprovalInstance, error) {
	return fake.instances, nil
}

func (fake *repositoryFake) FindReceipt(_ context.Context, tenantID, idempotencyKey string) (Receipt, bool, error) {
	receipt, ok := fake.receipts[tenantID+"|"+idempotencyKey]
	return receipt, ok, nil
}

func serviceFor(t *testing.T, repository *repositoryFake) *Service {
	t.Helper()
	counter := 0
	service, err := NewService(repository, func(time.Time) (string, error) {
		counter++
		return "01CRM" + string(rune('A'+counter%26)) + "00000000000000000000", nil
	}, func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// freezeSourceOwner 预置源负责人在该批次的 FROZEN 冻结：
// Transfer 只接受"属于这个交接批次"的责任项（BIZ-CRM-05）。
func freezeSourceOwner(repository *repositoryFake, tenantID, batchID, userID string) {
	repository.freezes[tenantID+"|"+batchID+"|"+userID] = Freeze{
		ID: "freeze-" + batchID, TenantID: tenantID, BatchID: batchID, UserID: userID, Status: FreezeFrozen,
	}
}

func TestFreezeIsIdempotentAndReportsResponsibilities(t *testing.T) {
	repository := newRepositoryFake("cus-1", "cus-2", "cus-3")
	service := serviceFor(t, repository)
	ctx := context.Background()

	first, count, err := service.Freeze(ctx, "tenant-1", "batch-1", "request-1", "user-leaver")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != FreezeFrozen || count != 3 {
		t.Fatalf("freeze = %+v count = %d", first, count)
	}
	second, count, err := service.Freeze(ctx, "tenant-1", "batch-1", "request-1", "user-leaver")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || count != 3 {
		t.Fatalf("repeated freeze must be idempotent: %+v count=%d", second, count)
	}
	if _, _, err := service.Freeze(ctx, "tenant-1", "batch-1", "request-1", "  "); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing user must be rejected: %v", err)
	}
}

func TestResponsibilitiesPaginate(t *testing.T) {
	repository := newRepositoryFake("cus-1", "cus-2", "cus-3")
	service := serviceFor(t, repository)

	items, next, err := service.Responsibilities(context.Background(), "tenant-1", "user-leaver", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || next != "cus-2" {
		t.Fatalf("page = %d next = %q", len(items), next)
	}
	if other, _, err := service.Responsibilities(context.Background(), "tenant-1", "user-other", "", 10); err != nil || len(other) != 0 {
		t.Fatalf("other users' responsibilities leaked: %d (%v)", len(other), err)
	}
}

func TestTransferIsIdempotentAndRejectsSelfSuccessor(t *testing.T) {
	repository := newRepositoryFake("cus-1")
	freezeSourceOwner(repository, "tenant-1", "batch-1", "user-leaver")
	service := serviceFor(t, repository)
	ctx := context.Background()

	input := TransferInput{TenantID: "tenant-1", BatchID: "batch-1", ItemID: "item-1", ResourceType: ResourceCustomer,
		ResourceID: "cus-1", FromUserID: "user-leaver", ToUserID: "user-successor", ExpectedVersion: 3,
		IdempotencyKey: "handover:batch-1:item-1"}
	receipt, err := service.Transfer(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != ReceiptApplied || receipt.AppliedVersion != 4 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if _, err := service.Transfer(ctx, input); err != nil {
		t.Fatalf("replay must succeed: %v", err)
	}
	if repository.transferCalls != 1 {
		t.Fatalf("transfer executed %d times, want exactly once", repository.transferCalls)
	}

	self := input
	self.IdempotencyKey = "handover:batch-1:item-2"
	self.ToUserID = self.FromUserID
	if _, err := service.Transfer(ctx, self); !errors.Is(err, ErrValidation) {
		t.Fatalf("self-successor must be rejected: %v", err)
	}
}

func TestTransferRejectsFrozenSuccessor(t *testing.T) {
	repository := newRepositoryFake("cus-1")
	freezeSourceOwner(repository, "tenant-1", "batch-1", "user-leaver")
	repository.freezes["tenant-1|batch-9|user-successor"] = Freeze{
		ID: "freeze-9", TenantID: "tenant-1", BatchID: "batch-9", UserID: "user-successor", Status: FreezeFrozen,
	}
	service := serviceFor(t, repository)

	_, err := service.Transfer(context.Background(), TransferInput{
		TenantID: "tenant-1", BatchID: "batch-1", ItemID: "item-1", ResourceType: ResourceCustomer, ResourceID: "cus-1",
		FromUserID: "user-leaver", ToUserID: "user-successor", ExpectedVersion: 3, IdempotencyKey: "handover:batch-1:item-1",
	})
	if !errors.Is(err, ErrFrozen) {
		t.Fatalf("err = %v, want ErrFrozen", err)
	}
	if repository.transferCalls != 0 {
		t.Fatal("transfer must not run when the successor is frozen")
	}
}

func TestReceiptLookupAndCompletion(t *testing.T) {
	repository := newRepositoryFake("cus-1")
	freezeSourceOwner(repository, "tenant-1", "batch-1", "user-leaver")
	service := serviceFor(t, repository)
	ctx := context.Background()

	if _, err := service.Receipt(ctx, "tenant-1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown receipt must be reported: %v", err)
	}
	if _, err := service.Transfer(ctx, TransferInput{TenantID: "tenant-1", BatchID: "batch-1", ItemID: "item-1",
		ResourceType: ResourceCustomer, ResourceID: "cus-1", FromUserID: "user-leaver", ToUserID: "user-successor",
		ExpectedVersion: 3, IdempotencyKey: "handover:batch-1:item-1"}); err != nil {
		t.Fatal(err)
	}
	receipt, err := service.Receipt(ctx, "tenant-1", "handover:batch-1:item-1")
	if err != nil || receipt.Status != ReceiptApplied {
		t.Fatalf("receipt = %+v (%v)", receipt, err)
	}
	if err := service.Complete(ctx, "tenant-1", "batch-1", "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	if len(repository.released) != 1 || repository.released[0] != "tenant-1|batch-1|COMPLETED" {
		t.Fatalf("released = %v", repository.released)
	}
	if err := service.Complete(ctx, "tenant-1", "batch-1", "SOMETHING"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid outcome must be rejected: %v", err)
	}
}

// BIZ-CRM-05：转移必须证明责任项属于输入的交接批次。
// 批次不存在、非本批次、已释放三种情形都必须 409 语义，且不写回执、不改数据。
func TestTransferRequiresFrozenBatchForSourceOwner(t *testing.T) {
	for _, test := range []struct {
		name    string
		freeze  *Freeze
		batchID string
	}{
		{name: "no freeze at all", batchID: "batch-1"},
		{name: "frozen by another batch", batchID: "batch-1", freeze: &Freeze{ID: "freeze-2", TenantID: "tenant-1", BatchID: "batch-2", UserID: "user-leaver", Status: FreezeFrozen}},
		{name: "released batch", batchID: "batch-1", freeze: &Freeze{ID: "freeze-1", TenantID: "tenant-1", BatchID: "batch-1", UserID: "user-leaver", Status: FreezeReleased}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newRepositoryFake("cus-1")
			if test.freeze != nil {
				repository.freezes["tenant-1|"+test.freeze.BatchID+"|user-leaver"] = *test.freeze
			}
			service := serviceFor(t, repository)
			input := TransferInput{TenantID: "tenant-1", BatchID: test.batchID, ItemID: "item-1", ResourceType: ResourceCustomer,
				ResourceID: "cus-1", FromUserID: "user-leaver", ToUserID: "user-successor", ExpectedVersion: 3,
				IdempotencyKey: "handover:" + test.batchID + ":item-1"}
			receipt, err := service.Transfer(context.Background(), input)
			if !errors.Is(err, ErrBatchNotFrozen) {
				t.Fatalf("err = %v, want ErrBatchNotFrozen", err)
			}
			if receipt != (Receipt{}) {
				t.Fatalf("receipt = %+v, want zero value", receipt)
			}
			if repository.transferCalls != 0 {
				t.Fatalf("transfer executed %d times, want none", repository.transferCalls)
			}
			// 落 REJECTED/RETRYABLE 回执会让同幂等键重放永久卡死，因此必须没有回执。
			if len(repository.receipts) != 0 {
				t.Fatalf("receipts = %v, want none", repository.receipts)
			}
			if _, found, lookupErr := repository.FindReceipt(context.Background(), "tenant-1", input.IdempotencyKey); lookupErr != nil || found {
				t.Fatalf("receipt found = %v err = %v, want absent", found, lookupErr)
			}
		})
	}
}

// 冻结被释放后，同幂等键重放仍然返回既有 APPLIED 回执，不得重新改派或改判 409。
func TestTransferReplayAfterReleaseReturnsExistingReceipt(t *testing.T) {
	repository := newRepositoryFake("cus-1")
	freezeSourceOwner(repository, "tenant-1", "batch-1", "user-leaver")
	service := serviceFor(t, repository)
	ctx := context.Background()
	input := TransferInput{TenantID: "tenant-1", BatchID: "batch-1", ItemID: "item-1", ResourceType: ResourceCustomer,
		ResourceID: "cus-1", FromUserID: "user-leaver", ToUserID: "user-successor", ExpectedVersion: 3,
		IdempotencyKey: "handover:batch-1:item-1"}
	first, err := service.Transfer(ctx, input)
	if err != nil || first.Status != ReceiptApplied {
		t.Fatalf("first transfer receipt = %+v err = %v", first, err)
	}
	if err := service.Complete(ctx, "tenant-1", "batch-1", "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Transfer(ctx, input)
	if err != nil {
		t.Fatalf("replay after release must return the committed receipt: %v", err)
	}
	if replayed != first {
		t.Fatalf("replayed receipt = %+v, want %+v", replayed, first)
	}
	if repository.transferCalls != 1 {
		t.Fatalf("transfer executed %d times, want exactly once", repository.transferCalls)
	}
}
