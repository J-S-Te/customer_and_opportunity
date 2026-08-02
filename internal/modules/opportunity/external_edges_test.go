package opportunity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type externalEdgesRepository struct {
	*GORMRepository
	opportunity     *Opportunity
	transferReplay  *ChangeIdempotency
	externalReplay  *ExternalLink
	approvedQuote   *ExternalLink
	lockCalls       int
	outboxCreates   int
	idemCreates     int
	externalCreates int
	stageUpdates    int
	stageLogs       int
}

type auditCapture struct {
	operation string
	result    string
	writes    int
}

func (w *auditCapture) Write(_ context.Context, event audit.Event) error {
	w.operation, w.result = event.Operation, event.Result
	w.writes++
	return nil
}

func (r *externalEdgesRepository) FindByID(context.Context, auth.Principal, uint64) (*Opportunity, error) {
	if r.opportunity == nil {
		return nil, ErrNotFound
	}
	copy := *r.opportunity
	return &copy, nil
}

func (r *externalEdgesRepository) FindByIDForUpdate(context.Context, auth.Principal, uint64) (*Opportunity, error) {
	r.lockCalls++
	return r.FindByID(context.Background(), auth.Principal{}, 0)
}

func (r *externalEdgesRepository) FindChangeIdempotency(context.Context, string, uint64, string, string, string) (*ChangeIdempotency, error) {
	return r.transferReplay, nil
}

func (r *externalEdgesRepository) FindChangeIdempotencyForUpdate(context.Context, string, uint64, string, string, string) (*ChangeIdempotency, error) {
	return r.transferReplay, nil
}

func (r *externalEdgesRepository) CreateChangeIdempotency(_ context.Context, value *ChangeIdempotency) error {
	r.idemCreates++
	r.transferReplay = value
	return nil
}

func (r *externalEdgesRepository) FindOutboxEvent(context.Context, string, string) (*OutboxEvent, error) {
	return nil, nil
}
func (r *externalEdgesRepository) CreateOutboxEvents(context.Context, []OutboxEvent) error {
	r.outboxCreates++
	return nil
}
func (r *externalEdgesRepository) FindExternalLink(context.Context, string, uint64, string, string) (*ExternalLink, error) {
	return r.externalReplay, nil
}
func (r *externalEdgesRepository) LatestExternalLink(context.Context, string, uint64) (*ExternalLink, error) {
	return r.externalReplay, nil
}
func (r *externalEdgesRepository) CreateExternalLink(_ context.Context, value *ExternalLink) error {
	r.externalCreates++
	r.externalReplay = value
	return nil
}
func (r *externalEdgesRepository) LatestApprovedQuotation(context.Context, string, uint64) (*ExternalLink, error) {
	return r.approvedQuote, nil
}
func (r *externalEdgesRepository) UpdateStage(_ context.Context, value *Opportunity, expected uint64) error {
	r.stageUpdates++
	value.Version = expected + 1
	r.opportunity = value
	return nil
}
func (r *externalEdgesRepository) CreateStageLog(context.Context, *StageLog) error {
	r.stageLogs++
	return nil
}

func signedOpportunity(version uint64) *Opportunity {
	contract := "HT-1"
	value := &Opportunity{OpportunityNo: "SJ202608010001", CustomerID: 3, ExpectedAmount: decimal.RequireFromString("100.00"), CurrentStage: StageSigned, Status: StatusClosed, ContractRef: &contract, TerminalPendingType: PendingNone}
	value.ID, value.TenantID, value.Version = 7, "tenant-a", version
	return value
}

func externalTestService(repo Repository) *Service {
	return &Service{
		repo: repo, audit: &countingAuditWriter{},
		now:                         func() time.Time { return time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC) },
		contractTransferTransaction: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
	}
}

func TestContractTransferExactReplayPrecedesChangedStateAndVersion(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(8)}
	accepted := ContractTransferResponse{OpportunityID: 7, EventVersion: 7, EventID: contractTransferEventID("tenant-a", 7, 7), DeliveryStatus: "PENDING"}
	encoded, _ := json.Marshal(accepted)
	repo.transferReplay = &ChangeIdempotency{RequestHash: contractTransferRequestHash(7, "转合同"), ResponseJSON: encoded}
	repo.opportunity.CurrentStage, repo.opportunity.Status, repo.opportunity.ContractRef = StageRequirement, StatusFollowing, nil
	service := externalTestService(repo)
	result, err := service.ContractTransfer(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, ContractTransferRequest{Version: 7, Reason: "转合同", IdempotencyKey: "key"})
	if err != nil || result.EventID != accepted.EventID {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if repo.lockCalls != 0 || repo.outboxCreates != 0 || repo.idemCreates != 0 {
		t.Fatalf("replay touched mutation path: %#v", repo)
	}
}

func TestContractTransferLocksAndRechecksStateBeforeOutbox(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(8)}
	service := externalTestService(repo)
	_, err := service.ContractTransfer(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, ContractTransferRequest{Version: 7, Reason: "转合同", IdempotencyKey: "key"})
	if !errors.Is(err, ErrVersionConflict) || repo.lockCalls != 1 || repo.outboxCreates != 0 || repo.idemCreates != 0 {
		t.Fatalf("err=%v repo=%#v", err, repo)
	}
}

func TestExternalCallbackReplayRequiresExactPayload(t *testing.T) {
	changed := time.Date(2026, 8, 1, 8, 0, 0, 123000000, time.UTC)
	amount := "100.00"
	input := ExternalStatusRequest{OpportunityID: 7, Type: "报价", SourceID: "BJ-1", Status: "报价已通过", SourceAmount: &amount, ChangedAt: changed}
	input, parsed, err := normalizeExternalStatus(input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := externalLinkSnapshot("tenant-a", 7, input, parsed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = validateExternalReplay(replay, input, parsed); err != nil {
		t.Fatal(err)
	}
	other := "101.00"
	input.SourceAmount = &other
	otherValue := decimal.RequireFromString(other)
	if !errors.Is(validateExternalReplay(replay, input, &otherValue), ErrIdempotencyConflict) {
		t.Fatal("different payload replay accepted")
	}
}

func TestExternalCallbackReplayBindsTerminalFields(t *testing.T) {
	changed := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	contract := "HT-A"
	input := ExternalStatusRequest{OpportunityID: 7, Type: "投标", SourceID: "TB-1", Status: "投标中标", ContractRef: &contract, ChangedAt: changed}
	input, amount, err := normalizeExternalStatus(input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := externalLinkSnapshot("tenant-a", 7, input, amount, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherContract := "HT-B"
	input.ContractRef = &otherContract
	if !errors.Is(validateExternalReplay(replay, input, amount), ErrIdempotencyConflict) {
		t.Fatal("different contract replay accepted")
	}
	input.ContractRef = &contract
	reason := "价格"
	input.LostReason = &reason
	if !errors.Is(validateExternalReplay(replay, input, amount), ErrIdempotencyConflict) {
		t.Fatal("different lost reason replay accepted")
	}
}

func TestExternalStageSourceIDIsStableAndFitsDatabaseColumn(t *testing.T) {
	value := externalStageSourceID(strings.Repeat("x", 64), strings.Repeat("y", 32))
	if len(value) != 63 || value != externalStageSourceID(strings.Repeat("x", 64), strings.Repeat("y", 32)) {
		t.Fatalf("source id is not a stable bounded digest: %q", value)
	}
	if value == externalStageSourceID(strings.Repeat("x", 64), "other") {
		t.Fatal("status is not bound to source digest")
	}
}

func TestStaleExternalCallbackIsAuditedWithoutMutation(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(7)}
	repo.opportunity.StageChangedAt = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writer := &auditCapture{}
	service := externalTestService(repo)
	service.audit = writer
	_, err := service.ApplyExternalStatus(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "machine-a"}), ExternalStatusRequest{OpportunityID: 7, Type: "报价", SourceID: "BJ-1", Status: "报价已通过", ChangedAt: repo.opportunity.StageChangedAt.Add(-time.Minute)})
	if !errors.Is(err, ErrStaleEvent) || writer.writes != 1 || writer.operation != "EXTERNAL_STATUS_STALE" || writer.result != "STALE" {
		t.Fatalf("err=%v audit=%#v", err, writer)
	}
	if repo.stageUpdates != 0 || repo.externalCreates != 0 || repo.stageLogs != 0 {
		t.Fatalf("stale event mutated state: %#v", repo)
	}
}

func TestQuoteAmountCheckWarnsWithoutChangingOpportunity(t *testing.T) {
	changedAt := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	approvedAmount := decimal.RequireFromString("101.00")
	model := signedOpportunity(7)
	repo := &externalEdgesRepository{
		GORMRepository: &GORMRepository{}, opportunity: model,
		approvedQuote: &ExternalLink{SourceID: "BJ-1", Status: "报价已通过", Amount: &approvedAmount, ChangedAt: changedAt},
	}
	service := externalTestService(repo)
	result, err := service.ExternalStatus(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a", ScopeMode: auth.ScopeAll}), 7)
	if err != nil {
		t.Fatal(err)
	}
	check := result.QuoteAmountCheck
	if check.Status != QuoteAmountCheckMismatch || !check.Warning || check.ExpectedAmount != "100.00" || check.ApprovedQuoteAmount == nil || *check.ApprovedQuoteAmount != "101.00" || check.OpportunityVersion != 7 {
		t.Fatalf("unexpected amount check: %#v", check)
	}
	if repo.stageUpdates != 0 || repo.externalCreates != 0 || repo.stageLogs != 0 || model.Version != 7 {
		t.Fatalf("read-only warning mutated opportunity: repo=%#v opportunity=%#v", repo, model)
	}
}

func TestQuoteAmountCheckDistinguishesMatchMissingAmountAndNoApproval(t *testing.T) {
	model := signedOpportunity(7)
	changedAt := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	matching := decimal.RequireFromString("100.00")
	for name, test := range map[string]struct {
		quote  *ExternalLink
		status string
	}{
		"match":          {quote: &ExternalLink{SourceID: "BJ-1", Amount: &matching, ChangedAt: changedAt}, status: QuoteAmountCheckMatch},
		"amount missing": {quote: &ExternalLink{SourceID: "BJ-1", ChangedAt: changedAt}, status: QuoteAmountCheckAmountMissing},
		"no approval":    {quote: nil, status: QuoteAmountCheckNoApprovedQuote},
	} {
		t.Run(name, func(t *testing.T) {
			result := quoteAmountCheck(model, test.quote)
			if result.Status != test.status || result.Warning {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	}
}

func TestExternalEdgesMigrationIsRegisteredAndDoesNotInventHistory(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000047_opportunity_external_edges.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000047_opportunity_external_edges.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{"CREATE TABLE crm_opportunity_external_links", "uq_opportunity_external_status", "snapshot_json JSON", "FOREIGN KEY (tenant_id,opportunity_id)"} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(text), "INSERT INTO CRM_OPPORTUNITY_EXTERNAL_LINKS") {
		t.Fatal("migration must not synthesize external history")
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS crm_opportunity_external_links") {
		t.Fatal("down migration missing table drop")
	}
}

var _ audit.Writer = (*countingAuditWriter)(nil)
