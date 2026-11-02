package opportunity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func contractTransferReplayRecord(version uint64) *ChangeIdempotency {
	accepted := ContractTransferResponse{OpportunityID: 7, EventVersion: version, EventID: contractTransferEventID("tenant-a", 7, version), DeliveryStatus: "PENDING"}
	encoded, _ := json.Marshal(accepted)
	return &ChangeIdempotency{RequestHash: contractTransferRequestHash(version, "转合同"), ResponseJSON: encoded}
}

func TestContractTransferFirstAttemptCreatesSingleEvent(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(7)}
	service := externalTestService(repo)
	result, err := service.ContractTransfer(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, ContractTransferRequest{Version: 7, Reason: "转合同", IdempotencyKey: "key"})
	if err != nil {
		t.Fatalf("first transfer rejected: %v", err)
	}
	if result.EventID != contractTransferEventID("tenant-a", 7, 7) || result.EventVersion != 7 || result.DeliveryStatus != "PENDING" {
		t.Fatalf("unexpected transfer result: %#v", result)
	}
	if repo.outboxCreates != 1 || repo.idemCreates != 1 || repo.lockCalls != 1 {
		t.Fatalf("first transfer must write exactly one event and replay record: %#v", repo)
	}
}

func TestContractTransferSameVersionReplayStaysIdempotentAfterLink(t *testing.T) {
	// 合同回写会推进商机版本并写入关联字段；同一 Idempotency-Key 的同版本重放
	// 仍必须返回原始受理结果，不能被“已关联”闸门改写成错误。
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(8)}
	repo.opportunity.ContractIntakeID = stringPtr("INTAKE-1")
	repo.opportunity.ContractID = stringPtr("01J8Z0YV3S9QK4M2N6P8R0T2V4")
	repo.opportunity.ContractLinkStatus = ContractLinkConfirmed
	repo.transferAccepted = true
	repo.transferReplay = contractTransferReplayRecord(7)
	service := externalTestService(repo)
	result, err := service.ContractTransfer(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, ContractTransferRequest{Version: 7, Reason: "转合同", IdempotencyKey: "key"})
	if err != nil || result.EventVersion != 7 || result.EventID != contractTransferEventID("tenant-a", 7, 7) {
		t.Fatalf("exact replay result=%#v err=%v", result, err)
	}
	if repo.lockCalls != 0 || repo.outboxCreates != 0 || repo.idemCreates != 0 {
		t.Fatalf("exact replay must not touch the mutation path: %#v", repo)
	}
}

func TestContractTransferRejectsAlreadyTransferredOpportunity(t *testing.T) {
	contractID := "01J8Z0YV3S9QK4M2N6P8R0T2V4"
	tests := []struct {
		name    string
		version uint64
		mutate  func(*Opportunity, *externalEdgesRepository)
	}{
		{
			name:    "accepted outbox event at the same version",
			version: 7,
			mutate: func(_ *Opportunity, repo *externalEdgesRepository) {
				repo.transferAccepted = true
			},
		},
		{
			name:    "accepted outbox event after the version advanced",
			version: 8,
			mutate: func(_ *Opportunity, repo *externalEdgesRepository) {
				repo.transferAccepted = true
			},
		},
		{
			name:    "contract intake id already written back",
			version: 7,
			mutate: func(model *Opportunity, _ *externalEdgesRepository) {
				model.ContractIntakeID = stringPtr("INTAKE-1")
			},
		},
		{
			name:    "contract id already written back",
			version: 7,
			mutate: func(model *Opportunity, _ *externalEdgesRepository) {
				model.ContractID = stringPtr(contractID)
			},
		},
		{
			name:    "contract link confirmed",
			version: 7,
			mutate: func(model *Opportunity, _ *externalEdgesRepository) {
				model.ContractLinkStatus = ContractLinkConfirmed
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(test.version)}
			test.mutate(repo.opportunity, repo)
			service := externalTestService(repo)
			_, err := service.ContractTransfer(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, ContractTransferRequest{Version: test.version, Reason: "转合同", IdempotencyKey: "second-key"})
			if !errors.Is(err, ErrContractTransferAlreadyLinked) {
				t.Fatalf("duplicate transfer error=%v, want %v", err, ErrContractTransferAlreadyLinked)
			}
			if repo.outboxCreates != 0 || repo.idemCreates != 0 {
				t.Fatalf("duplicate transfer mutated state: %#v", repo)
			}
		})
	}
}

func TestContractTransferRejectsWhenOutboxLookupFails(t *testing.T) {
	// 闸门查询失败不能退化为“允许转合同”：错误必须原样上抛。
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(7), acceptedErr: errors.New("outbox unavailable")}
	service := externalTestService(repo)
	_, err := service.ContractTransfer(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, ContractTransferRequest{Version: 7, Reason: "转合同", IdempotencyKey: "key"})
	if err == nil || err.Error() != "outbox unavailable" {
		t.Fatalf("lookup failure error=%v", err)
	}
	if repo.outboxCreates != 0 || repo.idemCreates != 0 {
		t.Fatalf("failed guard must not create events: %#v", repo)
	}
}

func TestContractTransferAcceptedIsAggregateScopedOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "qa-crm-transfer-" + time.Now().UTC().Format("150405000000")
	now := time.Now().UTC()
	// 同一商机的两次转合同会在版本推进后产生不同 event_id；
	// 闸门必须按聚合命中任意版本，而不是只命中最新版本。
	for _, version := range []uint64{7, 8} {
		event := &OutboxEvent{EventID: contractTransferEventID(tenant, 7, version), TenantID: tenant, EventType: contractTransferEventType, AggregateType: "opportunity", AggregateID: "7", Payload: []byte(`{}`), Status: "PENDING", CreatedAt: now}
		if err := db.Create(event).Error; err != nil {
			t.Fatal(err)
		}
	}
	other := &OutboxEvent{EventID: contractTransferEventID(tenant, 8, 7), TenantID: tenant, EventType: contractTransferEventType, AggregateType: "opportunity", AggregateID: "8", Payload: []byte(`{}`), Status: "PENDING", CreatedAt: now}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Where("tenant_id=?", tenant).Delete(&OutboxEvent{}).Error })
	repo := NewGORMRepository(db)
	if accepted, err := repo.ContractTransferAccepted(context.Background(), tenant, 7); err != nil || !accepted {
		t.Fatalf("accepted=%v err=%v, want true", accepted, err)
	}
	if accepted, err := repo.ContractTransferAccepted(context.Background(), tenant, 9); err != nil || accepted {
		t.Fatalf("accepted=%v err=%v, want false", accepted, err)
	}
}
