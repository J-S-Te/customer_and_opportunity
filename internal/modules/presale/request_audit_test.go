package presale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
)

// requestAuditRecorder 捕获售前申请主数据的审计事件，用于断言字段级前后值快照。
type requestAuditRecorder struct {
	events []audit.Event
	err    error
}

func (r *requestAuditRecorder) Write(_ context.Context, event audit.Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

func (r *requestAuditRecorder) only(t *testing.T) audit.Event {
	t.Helper()
	if len(r.events) != 1 {
		t.Fatalf("audit events = %d, want exactly 1: %+v", len(r.events), r.events)
	}
	return r.events[0]
}

func decodeAuditSnapshot(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	value := map[string]any{}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("audit snapshot is not a JSON object: %v (%s)", err, raw)
	}
	return value
}

func mustTime(t *testing.T, value any) time.Time {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("expected RFC3339 timestamp, got %#v", value)
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatalf("invalid timestamp %q: %v", text, err)
	}
	return parsed
}

// 重新提交是申请主数据唯一的更新入口：期望开始/结束时间会被整体覆盖，
// 因此必须留下 before/after 快照，否则“重开审批→再提交”造成的时间前移无法追溯。
func TestReopenRequestWritesFieldLevelUpdateAudit(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", UserName: "销售甲", Permissions: map[string]bool{"presale.create": true}, Roles: map[string]bool{}}
	originalStart := time.Date(2026, 8, 7, 18, 0, 0, 0, time.UTC)
	repo := &reopenRepository{
		request: &PresaleRequest{
			BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3},
			RequestNo: "TS202608080001", OpportunityID: 7, ApplicantID: actor.UserID,
			Status: StatusRejected, Venue: VenueOnsite, ServiceAddress: "旧地址",
			ContactName: "旧联系人", ContactPhoneMasked: "139****9999",
			ContactPhoneCipher: []byte("old-ciphertext"),
			Description:        "旧的售前支持需求说明",
			ExpectedStart:      originalStart, ExpectedEnd: originalStart.Add(2 * time.Hour),
			Urgency: UrgencyNormal,
		},
		approval: &ApprovalInstance{BaseModel: BaseModel{ID: 21, TenantID: actor.TenantID, Version: 2}, RequestID: 9, Status: "REJECTED"},
	}
	recorder := &requestAuditRecorder{}
	service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{at: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)}, fixedIDs{}).UseAuditWriter(recorder)
	input := validReopenInput()

	result, err := service.ReopenRequest(context.Background(), actor, 9, 3, input)
	if err != nil {
		t.Fatalf("ReopenRequest: %v", err)
	}
	event := recorder.only(t)
	if event.Module != "presale" || event.Operation != "UPDATE" || event.ResourceType != "presale_request" {
		t.Fatalf("unexpected audit identity: %+v", event)
	}
	if event.TenantID != actor.TenantID || event.ActorID != actor.UserID || event.ActorNameSnapshot != actor.UserName || event.Result != "SUCCESS" {
		t.Fatalf("unexpected audit actor: %+v", event)
	}
	if event.ResourceID != "9" {
		t.Fatalf("resource id = %q, want decimal request id", event.ResourceID)
	}

	before := decodeAuditSnapshot(t, event.BeforeJSON)
	after := decodeAuditSnapshot(t, event.AfterJSON)
	if got := mustTime(t, before["expected_start"]); !got.Equal(originalStart) {
		t.Fatalf("before_json expected_start = %s, want the pre-update value %s", got, originalStart)
	}
	if got := mustTime(t, before["expected_end"]); !got.Equal(originalStart.Add(2 * time.Hour)) {
		t.Fatalf("before_json expected_end = %s, want the pre-update value", got)
	}
	if got := mustTime(t, after["expected_start"]); !got.Equal(input.ExpectedStart.UTC()) {
		t.Fatalf("after_json expected_start = %s, want %s", got, input.ExpectedStart.UTC())
	}
	if got := mustTime(t, after["expected_end"]); !got.Equal(input.ExpectedEnd.UTC()) {
		t.Fatalf("after_json expected_end = %s, want %s", got, input.ExpectedEnd.UTC())
	}
	if before["expected_start"] == after["expected_start"] {
		t.Fatal("before_json and after_json must differ on the field this audit exists for")
	}
	if before["description"] != "旧的售前支持需求说明" || after["description"] != strings.TrimSpace(input.Description) {
		t.Fatalf("editable fields missing from snapshots: before=%#v after=%#v", before["description"], after["description"])
	}
	if after["venue"] != string(input.Venue) || after["contact_name"] != strings.TrimSpace(input.ContactName) || after["urgency"] != string(input.Urgency) {
		t.Fatalf("after_json lost editable fields: %#v", after)
	}
	if after["status"] != string(StatusPendingApproval) {
		t.Fatalf("after_json status = %#v, want %s", after["status"], StatusPendingApproval)
	}

	// after_json 必须就是本次返回给调用方的结果，避免审计与响应口径不一致。
	if want := audit.JSON(requestView(result)); !bytes.Equal(event.AfterJSON, want) {
		t.Fatalf("after_json = %s, want response projection %s", event.AfterJSON, want)
	}

	// 审计只允许出现掩码值：明文和密文都不能落进 before/after 快照。
	assertNoContactPhoneSecrets(t, event, "13900000000")
	if after["contact_phone"] != "138****0000" {
		t.Fatalf("after_json contact_phone = %#v, want masked value", after["contact_phone"])
	}
}

func TestCreateRequestWritesCreateAuditWithoutSecrets(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", UserName: "销售甲", Permissions: map[string]bool{"presale.create": true}}
	repo := &createNotificationRepo{}
	recorder := &requestAuditRecorder{}
	service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{at: time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)}, fixedIDs{}).UseAuditWriter(recorder)
	input := validCreateSecurityInput(7)

	created, err := service.CreateRequest(context.Background(), actor, "audit-create-key", input)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	event := recorder.only(t)
	if event.Module != "presale" || event.Operation != "CREATE" || event.ResourceType != "presale_request" || event.ResourceID != "1" {
		t.Fatalf("unexpected create audit: %+v", event)
	}
	if len(event.BeforeJSON) != 0 {
		t.Fatalf("create audit must not carry a before snapshot, got %s", event.BeforeJSON)
	}
	after := decodeAuditSnapshot(t, event.AfterJSON)
	if got := mustTime(t, after["expected_start"]); !got.Equal(input.ExpectedStart.UTC()) {
		t.Fatalf("after_json expected_start = %s, want %s", got, input.ExpectedStart.UTC())
	}
	if want := audit.JSON(requestView(created)); !bytes.Equal(event.AfterJSON, want) {
		t.Fatalf("after_json = %s, want response projection %s", event.AfterJSON, want)
	}
	assertNoContactPhoneSecrets(t, event, "13800000000")
}

// 幂等重放返回已提交结果，不得再写一条审计：否则按审计条数统计会高估真实变更次数。
func TestCreateRequestIdempotentReplayDoesNotDuplicateAudit(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", UserName: "销售甲", Permissions: map[string]bool{"presale.create": true}}
	repo := &createNotificationRepo{}
	recorder := &requestAuditRecorder{}
	service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{at: time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)}, fixedIDs{}).UseAuditWriter(recorder)
	input := validCreateSecurityInput(7)

	created, err := service.CreateRequest(context.Background(), actor, "replay-audit-key", input)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("audit events after create = %d, want 1", len(recorder.events))
	}
	// 让仓储像生产库一样按租户级幂等键返回已提交记录，第二次调用走重放分支。
	repo.createSecurityRepository.old = repo.request

	replayed, err := service.CreateRequest(context.Background(), actor, "replay-audit-key", input)
	if err != nil {
		t.Fatalf("replayed CreateRequest: %v", err)
	}
	if replayed == nil || replayed.ID != created.ID {
		t.Fatalf("replay = %+v, want the committed request %+v", replayed, created)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("idempotent replay wrote another audit event: %+v", recorder.events)
	}
}

// 未装配审计写入器时失败关闭：不允许状态变更成功但审计缺失。
func TestRequestMutationsFailClosedWithoutAuditWriter(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", UserName: "销售甲", Permissions: map[string]bool{"presale.create": true}, Roles: map[string]bool{}}

	t.Run("update", func(t *testing.T) {
		repo := &reopenRepository{
			request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS9", OpportunityID: 7, ApplicantID: actor.UserID, Status: StatusRejected},
			approval: &ApprovalInstance{BaseModel: BaseModel{ID: 21, TenantID: actor.TenantID, Version: 2}, RequestID: 9, Status: "REJECTED"},
		}
		service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{at: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)}, fixedIDs{})
		result, err := service.ReopenRequest(context.Background(), actor, 9, 3, validReopenInput())
		if result != nil || !errors.Is(err, ErrAuditUnavailable) {
			t.Fatalf("result=%+v error=%v, want audit unavailable", result, err)
		}
	})

	t.Run("create", func(t *testing.T) {
		service := NewService(&createNotificationRepo{}, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{}, fixedIDs{})
		created, err := service.CreateRequest(context.Background(), actor, "no-audit-writer", validCreateSecurityInput(7))
		if created != nil || !errors.Is(err, ErrAuditUnavailable) {
			t.Fatalf("created=%+v error=%v, want audit unavailable", created, err)
		}
	})
}

// 审计写入失败必须让整笔业务变更失败，而不是“业务成功、审计丢失”。
func TestRequestMutationsRollbackWhenAuditWriteFails(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", UserName: "销售甲", Permissions: map[string]bool{"presale.create": true}, Roles: map[string]bool{}}
	want := errors.New("audit storage unavailable")

	repo := &reopenRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS9", OpportunityID: 7, ApplicantID: actor.UserID, Status: StatusRejected},
		approval: &ApprovalInstance{BaseModel: BaseModel{ID: 21, TenantID: actor.TenantID, Version: 2}, RequestID: 9, Status: "REJECTED"},
	}
	service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{at: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)}, fixedIDs{}).UseAuditWriter(&requestAuditRecorder{err: want})

	result, err := service.ReopenRequest(context.Background(), actor, 9, 3, validReopenInput())
	if result != nil || !errors.Is(err, want) {
		t.Fatalf("result=%+v error=%v, want %v", result, err, want)
	}
}

// assertNoContactPhoneSecrets 断言审计快照既不含电话号码明文，也不含数据库里的密文原文。
func assertNoContactPhoneSecrets(t *testing.T, event audit.Event, plaintext string) {
	t.Helper()
	serialized := string(event.BeforeJSON) + string(event.AfterJSON)
	for name, secret := range map[string]string{
		"plaintext contact phone": plaintext,
		"ciphertext column":       "cipher",
		"legacy ciphertext":       "old-ciphertext",
	} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("audit snapshots leaked %s: %s", name, serialized)
		}
	}
	if strings.Contains(serialized, "contact_phone_cipher") {
		t.Fatalf("audit snapshots must not expose the cipher column name: %s", serialized)
	}
}
