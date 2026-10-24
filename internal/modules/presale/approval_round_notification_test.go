package presale

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

func approvalRoundTestNodes(t *testing.T) []byte {
	t.Helper()
	nodes, err := json.Marshal([]ApprovalNode{{ID: "sales", Name: "销售审批", Type: ApprovalNodeApproval, RoleCode: "sales_director"}})
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func TestApprovalRoundDiscriminatorIsStableWithinARoundAndChangesAfterReopen(t *testing.T) {
	firstStart := time.Date(2026, 8, 8, 1, 0, 0, 123456789, time.UTC)
	secondStart := firstStart.Add(time.Second)
	firstRound := approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &firstStart})
	if firstRound == "" {
		t.Fatal("approval round discriminator must not be empty")
	}
	if again := approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &firstStart}); again != firstRound {
		t.Fatalf("same round produced different markers: %q != %q", again, firstRound)
	}
	// 数据库列精度为毫秒，进程内时间与读回时间必须得到同一个轮次标识。
	persisted := firstStart.Truncate(time.Millisecond)
	if fromDatabase := approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &persisted}); fromDatabase != firstRound {
		t.Fatalf("persisted round marker changed: %q != %q", fromDatabase, firstRound)
	}
	secondRound := approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &secondStart})
	if secondRound == firstRound {
		t.Fatalf("new approval round reused old marker %q", firstRound)
	}

	notification := WorkflowNotification{
		TenantID: "tenant-a", Type: "PRESALE_APPROVAL_PENDING", RequestNo: "TS202608080001",
		RecipientID: "approver-1", RequestID: 9, ApprovalNode: 1, SourceDiscriminator: firstRound,
	}
	if retry := WorkflowNotificationSourceEventID(notification); retry != WorkflowNotificationSourceEventID(notification) {
		t.Fatal("same-round delivery retry must keep a stable source event id")
	}
	nextRound := notification
	nextRound.SourceDiscriminator = secondRound
	if WorkflowNotificationSourceEventID(nextRound) == WorkflowNotificationSourceEventID(notification) {
		t.Fatal("a reopened approval round must produce a new source event id")
	}
}

func TestReopenRequestUsesNewApprovalRoundNotificationKey(t *testing.T) {
	oldRoundStart := time.Date(2026, 8, 8, 0, 30, 0, 0, time.UTC)
	reopenAt := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", Permissions: map[string]bool{"presale.create": true}, Roles: map[string]bool{}}
	repo := &reopenRepository{
		request: &PresaleRequest{
			BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS202608080001",
			OpportunityID: 7, ApplicantID: actor.UserID, Status: StatusRejected,
		},
		approval: &ApprovalInstance{
			BaseModel: BaseModel{ID: 21, TenantID: actor.TenantID, Version: 2}, RequestID: 9,
			Status: "REJECTED", StartedAt: &oldRoundStart, NodesJSON: approvalRoundTestNodes(t),
		},
	}
	writer := &captureWorkflowNotifications{}
	catalog := &approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{"sales_director": {
		{ID: "approver-1", DisplayName: "销售总监甲"},
	}}}
	service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{at: reopenAt}, fixedIDs{}).
		UseOwnerDirectory(catalog).UseWorkflowNotifications(writer)

	if _, err := service.ReopenRequest(context.Background(), actor, 9, 3, validReopenInput()); err != nil {
		t.Fatalf("ReopenRequest: %v", err)
	}
	if len(writer.written) != 1 {
		t.Fatalf("notifications=%+v, want one new-round pending notification", writer.written)
	}
	notice := writer.written[0]
	if notice.RecipientID != "approver-1" || notice.Type != "PRESALE_APPROVAL_PENDING" || notice.ApprovalNode != 1 {
		t.Fatalf("unexpected reopened notification: %+v", notice)
	}
	if notice.SourceDiscriminator == "" || notice.SourceDiscriminator == approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &oldRoundStart}) {
		t.Fatalf("reopened notification kept the previous round marker: %q", notice.SourceDiscriminator)
	}
	if notice.SourceDiscriminator != approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &reopenAt}) {
		t.Fatalf("reopened notification marker=%q, want the new round start", notice.SourceDiscriminator)
	}
	oldKey := WorkflowNotificationSourceEventID(WorkflowNotification{
		TenantID: actor.TenantID, Type: "PRESALE_APPROVAL_PENDING", RequestNo: repo.request.RequestNo,
		RecipientID: "approver-1", RequestID: 9, ApprovalNode: 1,
		SourceDiscriminator: approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &oldRoundStart}),
	})
	if oldKey == WorkflowNotificationSourceEventID(notice) {
		t.Fatal("reopened approval round reused the previous source event id")
	}
}

func TestApprovalRejectionNotificationCarriesApprovalRound(t *testing.T) {
	roundStart := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)
	actor := Actor{TenantID: "tenant-a", UserID: "director-a", UserName: "销售总监", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	repo := &mutationRepository{
		request: &PresaleRequest{
			BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS9",
			ApplicantID: "sales-1", Status: StatusPendingApproval, CurrentApprovalNode: 1,
		},
		approval: &ApprovalInstance{
			EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING",
			StartedAt: &roundStart, NodesJSON: approvalRoundTestNodes(t),
		},
		replays: map[string]*MutationReplay{}, approvalLogs: map[string]*ApprovalLog{},
	}
	writer := &captureWorkflowNotifications{}
	service := NewService(repo, nil, nil, fixedClock{at: roundStart}, fixedIDs{}).UseWorkflowNotifications(writer)

	if err := service.RequestApprovalAction(context.Background(), actor, 9, "reject-round", ApprovalActionInput{Action: "REJECT", Comment: "资料不完整", Version: 3}); err != nil {
		t.Fatalf("RequestApprovalAction: %v", err)
	}
	notice, ok := findWorkflowNotification(writer.written, "sales-1", "PRESALE_APPROVAL_REJECTED")
	if !ok {
		t.Fatalf("notifications=%+v, want applicant rejection notice", writer.written)
	}
	if notice.SourceDiscriminator != approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &roundStart}) {
		t.Fatalf("rejection notice marker=%q", notice.SourceDiscriminator)
	}
	nextRoundStart := roundStart.Add(time.Minute)
	nextRound := notice
	nextRound.SourceDiscriminator = approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &nextRoundStart})
	if WorkflowNotificationSourceEventID(nextRound) == WorkflowNotificationSourceEventID(notice) {
		t.Fatal("a rejection after reopen would deduplicate against the previous round")
	}
}

func TestApprovalCallbackRejectionNotificationCarriesApprovalRound(t *testing.T) {
	roundStart := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)
	repo := &mutationRepository{
		request: &PresaleRequest{
			BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, RequestNo: "TS9",
			ApplicantID: "sales-1", Status: StatusPendingApproval, CurrentApprovalNode: 1,
		},
		approval: &ApprovalInstance{
			EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING", LastEventSeq: 10,
			PendingTaskID: "task-1", PendingApprover: "sales-1", PendingAction: "REJECT",
			StartedAt: &roundStart, NodesJSON: approvalRoundTestNodes(t),
		},
		approvalLogs: map[string]*ApprovalLog{}, replays: map[string]*MutationReplay{},
	}
	writer := &captureWorkflowNotifications{}
	service := NewService(repo, nil, nil, fixedClock{at: roundStart}, fixedIDs{}).UseWorkflowNotifications(writer)
	input := ApprovalCallbackInput{
		RequestID: 9, EngineInstanceID: "instance-1", EngineTaskID: "task-1", EventSequence: 11, Node: 1,
		Result: "REJECT", ApproverID: "sales-1", ApproverName: "销售总监", Comment: "资料不完整", OccurredAt: roundStart,
	}
	if err := service.HandleApprovalCallback(context.Background(), "tenant-a", input); err != nil {
		t.Fatalf("HandleApprovalCallback: %v", err)
	}
	notice, ok := findWorkflowNotification(writer.written, "sales-1", "PRESALE_APPROVAL_REJECTED")
	if !ok || notice.SourceDiscriminator != approvalRoundDiscriminator(&ApprovalInstance{StartedAt: &roundStart}) {
		t.Fatalf("callback rejection notice=%+v", writer.written)
	}
}
