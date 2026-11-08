package opportunity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type contractLinkBindingRepository struct {
	*externalEdgesRepository
	event       *OutboxEvent
	linkUpdates int
}

// FindOutboxEvent 返回按租户及事件绑定的转交快照替身。
// 参数：ctx为事务上下文，tenant为机器租户，event为原事件ID。
// 返回值：测试注入的事件或空值；不访问外部数据库。
func (r *contractLinkBindingRepository) FindOutboxEvent(_ context.Context, tenant, event string) (*OutboxEvent, error) {
	if r.event != nil && r.event.TenantID == tenant && r.event.EventID == event {
		return r.event, nil
	}
	return nil, nil
}

// UpdateContractLink 按旧同步版本记录关联结果。
// 参数：ctx为事务上下文，model为待保存商机，version为旧同步版本。
// 返回值：成功或同步版本冲突；不执行实际SQL。
func (r *contractLinkBindingRepository) UpdateContractLink(_ context.Context, model *Opportunity, version uint64) error {
	if version != r.opportunity.ContractSyncVersion {
		return ErrVersionConflict
	}
	r.linkUpdates++
	r.opportunity = model
	model.Version++
	return nil
}

// contractLinkBindingService 构造保留真实服务事务与Handler的线协议测试。
// 参数：t为测试上下文，ref为已通过签约验真的原合同引用。
// 返回值：服务、持久化端口替身及可信终态回调输入。
func contractLinkBindingService(t *testing.T, ref string) (*Service, *contractLinkBindingRepository, ContractLinkRequest) {
	t.Helper()
	model := signedOpportunity(7)
	model.ContractRef = stringPtr(ref)
	eventID := contractTransferEventID("tenant-a", 7, 7)
	payload, err := json.Marshal(map[string]any{"opportunity_id": 7, "customer_id": 3, "contract_ref": ref, "event_version": 7})
	if err != nil {
		t.Fatal(err)
	}
	repo := &contractLinkBindingRepository{externalEdgesRepository: &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: model},
		event: &OutboxEvent{EventID: eventID, TenantID: "tenant-a", EventType: contractTransferEventType, AggregateType: "opportunity", AggregateID: "7", Payload: payload, Status: "SENT"}}
	service := externalTestService(repo)
	service.db = provisionDryRunDB(t)
	linked := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	input := ContractLinkRequest{EventID: eventID, IntakeID: "intake-7", ContractID: "01K00000000000000000000000", ContractNumber: "HT-7", Status: "LINK_CONFIRMED", LinkedAt: &linked, SyncVersion: 2}
	return service, repo, input
}

// TestContractLinkAcceptsVerifiedIDOrNumber 验证可信原事件下合同ID与编号都能关联。
// 参数：t为测试上下文，覆盖同事件重试及非法引用。
// 返回值：无；合法ID被拒绝、非法引用被接受或重放重复写入时失败。
func TestContractLinkAcceptsVerifiedIDOrNumber(t *testing.T) {
	for _, ref := range []string{"01K00000000000000000000000", "HT-7", "HT-other"} {
		t.Run(ref, func(t *testing.T) {
			service, repo, input := contractLinkBindingService(t, ref)
			ctx := createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "machine:contract", ScopeMode: auth.ScopeAll})
			result, err := service.ContractLinkCallback(ctx, 7, input)
			if ref == "HT-other" {
				if err == nil || repo.linkUpdates != 0 {
					t.Fatalf("非匹配合同引用被接受: result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || result.ContractID == nil || *result.ContractID != input.ContractID {
				t.Fatalf("合法原合同引用关联失败: result=%+v err=%v", result, err)
			}
			if _, err = service.ContractLinkCallback(ctx, 7, input); err != nil || repo.linkUpdates != 1 {
				t.Fatalf("原事件重放非幂等: writes=%d err=%v", repo.linkUpdates, err)
			}
		})
	}
}

// TestContractLinkRejectsUnboundCallback 验证身份别名不能绕过原转交事件归属。
// 参数：t为测试上下文，逐项破坏事件、租户、客户、商机或接入记录绑定。
// 返回值：无；任何不可信关联产生写入时失败。
func TestContractLinkRejectsUnboundCallback(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*contractLinkBindingRepository, *ContractLinkRequest)
	}{
		{"missing_event", func(r *contractLinkBindingRepository, _ *ContractLinkRequest) { r.event = nil }},
		{"wrong_tenant", func(r *contractLinkBindingRepository, _ *ContractLinkRequest) { r.event.TenantID = "tenant-other" }},
		{"wrong_event", func(_ *contractLinkBindingRepository, in *ContractLinkRequest) { in.EventID = "other-event" }},
		{"wrong_opportunity", func(r *contractLinkBindingRepository, _ *ContractLinkRequest) { r.event.AggregateID = "8" }},
		{"wrong_customer", func(r *contractLinkBindingRepository, _ *ContractLinkRequest) {
			r.event.Payload = []byte(`{"opportunity_id":7,"customer_id":4,"contract_ref":"HT-7","event_version":7}`)
		}},
		{"wrong_payload_ref", func(r *contractLinkBindingRepository, _ *ContractLinkRequest) {
			r.event.Payload = []byte(`{"opportunity_id":7,"customer_id":3,"contract_ref":"other","event_version":7}`)
		}},
		{"wrong_intake", func(r *contractLinkBindingRepository, _ *ContractLinkRequest) {
			r.opportunity.ContractIntakeID = stringPtr("another-intake")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, repo, input := contractLinkBindingService(t, "HT-7")
			tc.mutate(repo, &input)
			_, err := service.ContractLinkCallback(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "machine:contract", ScopeMode: auth.ScopeAll}), 7, input)
			if err == nil || repo.linkUpdates != 0 || repo.idemCreates != 0 {
				t.Fatalf("未绑定回调被接受: err=%v writes=%d idem=%d", err, repo.linkUpdates, repo.idemCreates)
			}
		})
	}
}

// TestContractLinkProductionWireCrossRepository 消费合同真实编码的字节并驱动CRM真实Handler。
// 参数：t为测试上下文，CRM_LINK_CONTRACT_FILE可指定生产编码导出的合成回调。
// 返回值：无；合法载荷被严格解码拒绝或路径与载荷身份不一致仍写入时失败。
func TestContractLinkProductionWireCrossRepository(t *testing.T) {
	service, repo, input := contractLinkBindingService(t, "01K00000000000000000000000")
	raw := []byte(`{"opportunity_id":7,"event_id":"` + input.EventID + `","intake_id":"intake-7","contract_id":"01K00000000000000000000000","contract_number":"HT-7","status":"LINK_CONFIRMED","linked_at":"2026-11-08T00:00:00Z","sync_version":2}`)
	if path := os.Getenv("CRM_LINK_CONTRACT_FILE"); path != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	router.POST("/api/v1/internal/opportunities/:id/contract-link", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{TenantID: "tenant-a", UserID: "machine:contract", ScopeMode: auth.ScopeAll}))
		(&Handler{service: service}).ContractLinkCallback(c)
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/opportunities/7/contract-link", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", input.EventID)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || repo.linkUpdates != 1 {
		t.Fatalf("生产合同回调失败: status=%d body=%s writes=%d", recorder.Code, recorder.Body.String(), repo.linkUpdates)
	}
	mismatch := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/opportunities/8/contract-link", strings.NewReader(string(raw)))
	mismatch.Header.Set("Content-Type", "application/json")
	mismatch.Header.Set("Idempotency-Key", input.EventID)
	denied := httptest.NewRecorder()
	router.ServeHTTP(denied, mismatch)
	if denied.Code != http.StatusUnprocessableEntity || repo.linkUpdates != 1 {
		t.Fatalf("路径商机与可信载荷不一致仍处理: status=%d body=%s writes=%d",
			denied.Code, denied.Body.String(), repo.linkUpdates)
	}
}
