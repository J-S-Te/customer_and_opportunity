package presale

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

type startedNotificationRepo struct {
	Repository
	request  *PresaleRequest
	instance *ApprovalInstance
}

type createNotificationRepo struct {
	createSecurityRepository
	request  *PresaleRequest
	instance *ApprovalInstance
}

func (*createNotificationRepo) NextRequestNo(context.Context, string, time.Time) (string, error) {
	return "TS202608230001", nil
}
func (r *createNotificationRepo) CreateRequest(_ context.Context, value *PresaleRequest) error {
	value.ID = 1
	copyValue := *value
	r.request = &copyValue
	return nil
}
func (r *createNotificationRepo) CreateApprovalInstance(_ context.Context, value *ApprovalInstance) error {
	copyValue := *value
	r.instance = &copyValue
	return nil
}

func (r *startedNotificationRepo) WithTransaction(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (r *startedNotificationRepo) FindRequestForUpdate(_ context.Context, _ string, _ uint64) (*PresaleRequest, error) {
	return r.request, nil
}
func (r *startedNotificationRepo) FindApprovalInstanceForUpdate(_ context.Context, _ string, _ uint64) (*ApprovalInstance, error) {
	return r.instance, nil
}
func (r *startedNotificationRepo) UpdateApprovalInstance(_ context.Context, _ *ApprovalInstance, _ map[string]any) error {
	return nil
}
func (r *startedNotificationRepo) UpdateRequestVersioned(_ context.Context, _ *PresaleRequest, _ uint64, _ map[string]any) error {
	return nil
}
func (r *startedNotificationRepo) CreateStatusLog(_ context.Context, _ *StatusLog) error { return nil }

type captureWorkflowNotifications struct{ written []WorkflowNotification }

func (w *captureWorkflowNotifications) Write(_ context.Context, n WorkflowNotification) error {
	w.written = append(w.written, n)
	return nil
}

type failingWorkflowNotifications struct{ err error }

func (w failingWorkflowNotifications) Write(context.Context, WorkflowNotification) error {
	return w.err
}

type approvalRoleCatalog struct {
	usersByRole map[string][]ownerdirectory.User
	err         error
	queries     []ownerdirectory.Query
}

func (c *approvalRoleCatalog) List(_ context.Context, query ownerdirectory.Query) (ownerdirectory.Page, error) {
	c.queries = append(c.queries, query)
	if c.err != nil {
		return ownerdirectory.Page{}, c.err
	}
	role := ""
	if len(query.RoleCodes) > 0 {
		role = query.RoleCodes[0]
	}
	users := append([]ownerdirectory.User(nil), c.usersByRole[role]...)
	return ownerdirectory.Page{Items: users, Page: query.Page, PageSize: query.PageSize, Total: int64(len(users))}, nil
}
func (*approvalRoleCatalog) Validate(context.Context, string, string) error { return nil }
func (*approvalRoleCatalog) Resolve(context.Context, []string) (map[string]ownerdirectory.User, error) {
	return nil, nil
}

func newStartedService(writer WorkflowNotificationWriter, catalog ownerdirectory.Catalog) *Service {
	repo := &startedNotificationRepo{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 1, TenantID: "t-1", Version: 1}, RequestNo: "PR-1", Status: StatusApprovalStarting},
		instance: &ApprovalInstance{RequestID: 1, LastEventSeq: 0},
	}
	return NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{}).UseWorkflowNotifications(writer).UseOwnerDirectory(catalog)
}

func TestMarkApprovalStartedNotifiesEveryActiveFirstNodeApprover(t *testing.T) {
	writer := &captureWorkflowNotifications{}
	catalog := &approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{"sales_director": {
		{ID: "approver-1", DisplayName: "张三"},
		{ID: "approver-2", DisplayName: "李四"},
	}}}
	service := newStartedService(writer, catalog)

	err := service.MarkApprovalStarted(context.Background(), "t-1", ApprovalStartedInput{
		RequestID: 1, EngineInstanceID: "eng-1", EventSequence: 1,
		NextApproverID: "approver-1", NextApproverName: "张三",
	})
	if err != nil {
		t.Fatalf("MarkApprovalStarted: %v", err)
	}
	if len(writer.written) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(writer.written))
	}
	for index, recipientID := range []string{"approver-1", "approver-2"} {
		n := writer.written[index]
		if n.RecipientID != recipientID || n.Type != "PRESALE_APPROVAL_PENDING" || n.RequestID != 1 || n.RequestNo != "PR-1" {
			t.Fatalf("unexpected notification: %+v", n)
		}
	}
	if len(catalog.queries) != 1 || len(catalog.queries[0].RoleCodes) != 1 || catalog.queries[0].RoleCodes[0] != "sales_director" {
		t.Fatalf("unexpected directory query: %+v", catalog.queries)
	}
}

func TestMarkApprovalStartedFailsClosedWithoutActiveApprover(t *testing.T) {
	writer := &captureWorkflowNotifications{}
	service := newStartedService(writer, &approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{}})

	err := service.MarkApprovalStarted(context.Background(), "t-1", ApprovalStartedInput{
		RequestID: 1, EngineInstanceID: "eng-1", EventSequence: 1,
	})
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("MarkApprovalStarted error=%v, want dependency unavailable", err)
	}
	if len(writer.written) != 0 {
		t.Fatalf("expected no notification, got %+v", writer.written)
	}
}

func TestMarkApprovalStartedReturnsNotificationWriteError(t *testing.T) {
	want := errors.New("notification database unavailable")
	service := newStartedService(failingWorkflowNotifications{err: want}, &approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{"sales_director": {{ID: "approver-1", DisplayName: "张三"}}}})

	err := service.MarkApprovalStarted(context.Background(), "t-1", ApprovalStartedInput{
		RequestID: 1, EngineInstanceID: "eng-1", EventSequence: 1,
		NextApproverID: "approver-1", NextApproverName: "张三",
	})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want %v", err, want)
	}
}

func TestInternalApprovalStartNotifiesEveryActiveFirstNodeApprover(t *testing.T) {
	repo := &createNotificationRepo{}
	writer := &captureWorkflowNotifications{}
	catalog := &approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{"sales_director": {
		{ID: "sales-director-1", DisplayName: "销售总监甲"},
		{ID: "sales-director-2", DisplayName: "销售总监乙"},
	}}}
	service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{at: time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)}, fixedIDs{}).
		UseOwnerDirectory(catalog).UseWorkflowNotifications(writer)
	actor := Actor{TenantID: "tenant-a", UserID: "sales-1", UserName: "销售", Permissions: map[string]bool{"presale.create": true}}

	created, err := service.CreateRequest(context.Background(), actor, "create-with-notification", validCreateSecurityInput(7))
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if created == nil || repo.instance == nil || len(writer.written) != 2 {
		t.Fatalf("created=%+v instance=%+v notifications=%+v", created, repo.instance, writer.written)
	}
	if writer.written[0].RecipientID != "sales-director-1" || writer.written[1].RecipientID != "sales-director-2" {
		t.Fatalf("unexpected recipients: %+v", writer.written)
	}
	for _, notification := range writer.written {
		if notification.TenantID != actor.TenantID || notification.Type != "PRESALE_APPROVAL_PENDING" ||
			notification.Title != "售前审批待处理" || notification.RequestID != created.ID ||
			notification.RequestNo != created.RequestNo || !strings.Contains(notification.Body, "流转到您当前审批节点") {
			t.Fatalf("unexpected first-node notification contract: %+v", notification)
		}
	}
	if len(catalog.queries) != 1 || len(catalog.queries[0].RoleCodes) != 1 || catalog.queries[0].RoleCodes[0] != "sales_director" {
		t.Fatalf("submission did not resolve the sales director role: %+v", catalog.queries)
	}
}

func TestInternalApprovalStartFailsClosedWithoutActiveFirstNodeApprover(t *testing.T) {
	repo := &createNotificationRepo{}
	writer := &captureWorkflowNotifications{}
	service := NewService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}}, testPhoneProtector{}, fixedClock{}, fixedIDs{}).
		UseOwnerDirectory(&approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{}}).UseWorkflowNotifications(writer)
	actor := Actor{TenantID: "tenant-a", UserID: "sales-1", UserName: "销售", Permissions: map[string]bool{"presale.create": true}}

	created, err := service.CreateRequest(context.Background(), actor, "create-without-approver", validCreateSecurityInput(7))
	if created != nil || !errors.Is(err, ErrDependencyUnavailable) || len(writer.written) != 0 {
		t.Fatalf("created=%+v error=%v notifications=%+v", created, err, writer.written)
	}
}

func TestInternalApprovalPassNotifiesEveryActiveNextNodeApprover(t *testing.T) {
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "sales", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "technical", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{TenantID: "tenant-a", UserID: "sales-1", UserName: "销售总监", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	repo := &mutationRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS9", Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval: &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING", NodesJSON: nodes},
		replays:  map[string]*MutationReplay{},
	}
	writer := &captureWorkflowNotifications{}
	catalog := &approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{"technical_director": {
		{ID: "technical-1", DisplayName: "技术总监甲"},
		{ID: "technical-2", DisplayName: "技术总监乙"},
	}}}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{}).
		UseOwnerDirectory(catalog).UseWorkflowNotifications(writer)

	err = service.RequestApprovalAction(context.Background(), actor, 9, "pass-node-1", ApprovalActionInput{Action: "PASS", Version: 3})
	if err != nil {
		t.Fatalf("RequestApprovalAction: %v", err)
	}
	if len(writer.written) != 2 || writer.written[0].RecipientID != "technical-1" || writer.written[1].RecipientID != "technical-2" {
		t.Fatalf("unexpected notifications: %+v", writer.written)
	}
	for _, notification := range writer.written {
		if notification.TenantID != actor.TenantID || notification.Type != "PRESALE_APPROVAL_PENDING" ||
			notification.Title != "售前审批待处理" || notification.RequestID != repo.request.ID ||
			notification.RequestNo != repo.request.RequestNo || !strings.Contains(notification.Body, "流转到您当前审批节点") {
			t.Fatalf("unexpected next-node notification contract: %+v", notification)
		}
	}
	if len(catalog.queries) != 1 || catalog.queries[0].RoleCodes[0] != "technical_director" {
		t.Fatalf("unexpected directory query: %+v", catalog.queries)
	}
}

func TestInternalApprovalTerminalActionsNotifyApplicant(t *testing.T) {
	nodes, err := json.Marshal([]ApprovalNode{{ID: "sales", Type: ApprovalNodeApproval, RoleCode: "sales_director"}})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		action    string
		comment   string
		wantType  string
		wantTitle string
		wantBody  string
	}{
		{name: "pass", action: "PASS", wantType: "PRESALE_APPROVAL_APPROVED", wantTitle: "售前审批已通过", wantBody: "您的售前申请已完成审批。"},
		{name: "reject", action: "REJECT", comment: "资料不完整", wantType: "PRESALE_APPROVAL_REJECTED", wantTitle: "售前审批已驳回", wantBody: "您的售前申请已被驳回：资料不完整"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actor := Actor{TenantID: "tenant-a", UserID: "director-a", UserName: "销售总监", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
			repo := &mutationRepository{
				request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, ApplicantID: "sales-1", RequestNo: "TS9", Status: StatusPendingApproval, CurrentApprovalNode: 1},
				approval: &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING", NodesJSON: nodes},
				replays:  map[string]*MutationReplay{}, approvalLogs: map[string]*ApprovalLog{},
			}
			writer := &captureWorkflowNotifications{}
			service := NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)}, fixedIDs{}).UseWorkflowNotifications(writer)

			if err := service.RequestApprovalAction(context.Background(), actor, 9, "terminal-"+test.name, ApprovalActionInput{Action: test.action, Comment: test.comment, Version: 3}); err != nil {
				t.Fatalf("RequestApprovalAction: %v", err)
			}
			if len(writer.written) != 1 {
				t.Fatalf("notifications=%+v, want one applicant notification", writer.written)
			}
			notice := writer.written[0]
			if notice.TenantID != actor.TenantID || notice.RecipientID != "sales-1" || notice.Type != test.wantType || notice.Title != test.wantTitle || notice.Body != test.wantBody || notice.RequestID != 9 || notice.RequestNo != "TS9" {
				t.Fatalf("notification=%+v", notice)
			}
		})
	}
}

func TestInternalApprovalPassFailsClosedWithoutActiveNextNodeApprover(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-1", UserName: "销售总监", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	repo := &mutationRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS9", Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval: &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING"},
		replays:  map[string]*MutationReplay{},
	}
	writer := &captureWorkflowNotifications{}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{}).
		UseOwnerDirectory(&approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{}}).UseWorkflowNotifications(writer)

	err := service.RequestApprovalAction(context.Background(), actor, 9, "pass-node-1", ApprovalActionInput{Action: "PASS", Version: 3})
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("error=%v, want dependency unavailable", err)
	}
	if len(writer.written) != 0 || len(repo.replays) != 0 || repo.requestUpdates != 0 {
		t.Fatalf("workflow advanced without recipient: notifications=%d replays=%d updates=%d", len(writer.written), len(repo.replays), repo.requestUpdates)
	}
}

func TestApprovalCallbackPassResolvesCurrentActiveNextNodeApprovers(t *testing.T) {
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "sales", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "technical", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &mutationRepository{
		request:      &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, RequestNo: "TS9", Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval:     &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING", LastEventSeq: 10, PendingTaskID: "task-1", PendingApprover: "sales-1", PendingAction: "PASS", NodesJSON: nodes},
		approvalLogs: map[string]*ApprovalLog{}, replays: map[string]*MutationReplay{},
	}
	writer := &captureWorkflowNotifications{}
	catalog := &approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{"technical_director": {
		{ID: "technical-1", DisplayName: "技术总监甲"},
		{ID: "technical-2", DisplayName: "技术总监乙"},
	}}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).UseOwnerDirectory(catalog).UseWorkflowNotifications(writer)
	input := ApprovalCallbackInput{RequestID: 9, EngineInstanceID: "instance-1", EngineTaskID: "task-1", EventSequence: 11, Node: 1, Result: "PASS", ApproverID: "sales-1", ApproverName: "销售总监", OccurredAt: time.Now().UTC()}

	if err = service.HandleApprovalCallback(context.Background(), "tenant-a", input); err != nil {
		t.Fatalf("HandleApprovalCallback: %v", err)
	}
	if len(writer.written) != 2 || writer.written[0].RecipientID != "technical-1" || writer.written[1].RecipientID != "technical-2" {
		t.Fatalf("unexpected notifications: %+v", writer.written)
	}
}

func TestApprovalCallbackPassFailsClosedWithoutActiveNextNodeApprover(t *testing.T) {
	repo := &mutationRepository{
		request:      &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, RequestNo: "TS9", Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval:     &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING", LastEventSeq: 10, PendingTaskID: "task-1", PendingApprover: "sales-1", PendingAction: "PASS"},
		approvalLogs: map[string]*ApprovalLog{}, replays: map[string]*MutationReplay{},
	}
	writer := &captureWorkflowNotifications{}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).
		UseOwnerDirectory(&approvalRoleCatalog{usersByRole: map[string][]ownerdirectory.User{}}).UseWorkflowNotifications(writer)
	input := ApprovalCallbackInput{RequestID: 9, EngineInstanceID: "instance-1", EngineTaskID: "task-1", EventSequence: 11, Node: 1, Result: "PASS", ApproverID: "sales-1", ApproverName: "销售总监", OccurredAt: time.Now().UTC()}

	err := service.HandleApprovalCallback(context.Background(), "tenant-a", input)
	if !errors.Is(err, ErrDependencyUnavailable) || len(writer.written) != 0 {
		t.Fatalf("error=%v notifications=%d", err, len(writer.written))
	}
}
