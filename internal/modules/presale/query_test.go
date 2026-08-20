package presale

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

type queryRepository struct {
	Repository
	request          *PresaleRequest
	assignments      []Assignment
	worklogs         []Worklog
	receivedScope    RequestQueryScope
	receivedQueries  []RequestListQuery
	aggregateCalls   int
	worklogListCalls int
	opportunityPage  RequestListPage
	historical       map[uint64]bool
	filterOptions    RequestFilterOptions
	approval         *ApprovalInstance
}

func (r *queryRepository) FindApprovalInstanceForUpdate(context.Context, string, uint64) (*ApprovalInstance, error) {
	if r.approval == nil {
		return nil, ErrNotFound
	}
	value := *r.approval
	return &value, nil
}

func (r *queryRepository) FindApprovalInstance(context.Context, string, uint64) (*ApprovalInstance, error) {
	if r.approval == nil {
		return nil, ErrNotFound
	}
	value := *r.approval
	return &value, nil
}

func (r *queryRepository) FindRequest(_ context.Context, tenant string, id uint64) (*PresaleRequest, error) {
	if r.request == nil || r.request.TenantID != tenant || r.request.ID != id {
		return nil, ErrNotFound
	}
	return r.request, nil
}

func (r *queryRepository) ListAssignments(_ context.Context, tenant string, requestID uint64) ([]Assignment, error) {
	if r.request == nil || tenant != r.request.TenantID || requestID != r.request.ID {
		return nil, ErrNotFound
	}
	return r.assignments, nil
}

func (r *queryRepository) ListRequests(_ context.Context, scope RequestQueryScope, query RequestListQuery, _ time.Time) (RequestListPage, error) {
	r.receivedScope = scope
	r.receivedQueries = append(r.receivedQueries, query)
	return RequestListPage{Items: []RequestListItem{{ID: 1, ApplicantID: "sales-1", Status: StatusPendingApproval}}, Page: query.Page, PageSize: query.PageSize, Total: 1}, nil
}

func (r *queryRepository) ListRequestFilterOptions(_ context.Context, scope RequestQueryScope, query RequestListQuery, _ time.Time, limit int) (RequestFilterOptions, error) {
	r.receivedScope = scope
	r.receivedQueries = append(r.receivedQueries, query)
	if limit != filterOptionLimit {
		return RequestFilterOptions{}, errors.New("unexpected option limit")
	}
	return r.filterOptions, nil
}

func (r *queryRepository) ListCurrentAssignments(_ context.Context, _ string, ids []uint64) (map[uint64][]Assignment, error) {
	result := make(map[uint64][]Assignment)
	if len(ids) > 0 {
		result[ids[0]] = r.assignments
	}
	return result, nil
}

func (r *queryRepository) RequestAggregate(context.Context, string, uint64) (RequestAggregate, error) {
	r.aggregateCalls++
	return RequestAggregate{TotalWorkHours: "8.00"}, nil
}

func (r *queryRepository) AlertAggregate(context.Context, string, uint64) (AlertAggregate, error) {
	return AlertAggregate{Level: "NONE"}, nil
}

func (r *queryRepository) ListWorklogs(context.Context, string, uint64) ([]Worklog, error) {
	r.worklogListCalls++
	return r.worklogs, nil
}

func (r *queryRepository) ListOpportunityRequests(_ context.Context, tenant string, opportunityID uint64, page, pageSize int, _ time.Time) (RequestListPage, error) {
	if tenant != "tenant-1" || opportunityID == 0 {
		return RequestListPage{}, ErrNotFound
	}
	value := r.opportunityPage
	value.Page, value.PageSize = page, pageSize
	return value, nil
}

func (r *queryRepository) HistoricalAssignmentRequestIDs(_ context.Context, _ string, _ string, _ []uint64) (map[uint64]bool, error) {
	return r.historical, nil
}

func (r *queryRepository) LatestProgressByRequest(_ context.Context, _ string, _ []uint64) (map[uint64]string, error) {
	return map[uint64]string{7: "latest progress"}, nil
}

func TestListRequestsDerivesRoleScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		actor         Actor
		all           bool
		wantApplicant bool
		wantAssignee  bool
	}{
		{name: "sales person receives applications and assignments", actor: actorWithRoles("sales-1", "person-1", "sales"), wantApplicant: true, wantAssignee: true},
		{name: "technician sees assignments", actor: actorWithRoles("tech-1", "person-1", "technician"), wantAssignee: true},
		{name: "sales technician gets documented union", actor: actorWithRoles("sales-1", "person-1", "sales", "technician"), wantApplicant: true, wantAssignee: true},
		{name: "authoritative person identity sees assignments without CRM role inference", actor: readableActor("other-1", "person-1"), wantAssignee: true},
		{name: "missing person identity fails closed", actor: readableActor("other-1", "")},
		{name: "team lead gets tenant scope", actor: managerActor("team_lead"), all: true},
		{name: "technical director gets tenant scope", actor: managerActor("technical_director"), all: true},
		{name: "crm super admin gets tenant scope", actor: managerActor("crm_super_admin"), all: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repo := &queryRepository{}
			service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
			if _, err := service.ListRequests(context.Background(), test.actor, RequestListQuery{Page: 1, PageSize: 20}); err != nil {
				t.Fatalf("ListRequests() error: %v", err)
			}
			if repo.receivedScope.All != test.all || repo.receivedScope.TenantID != test.actor.TenantID {
				t.Fatalf("scope=%+v, want all=%v tenant=%s", repo.receivedScope, test.all, test.actor.TenantID)
			}
			if got := repo.receivedScope.ApplicantID != ""; got != test.wantApplicant {
				t.Fatalf("applicant scope=%+v, want enabled=%v", repo.receivedScope, test.wantApplicant)
			}
			if got := repo.receivedScope.AssigneeID != ""; got != test.wantAssignee {
				t.Fatalf("assignee scope=%+v, want enabled=%v", repo.receivedScope, test.wantAssignee)
			}
		})
	}
}

func TestPrepareRequestListQueryRejectsUnknownEnumsSortAndClosedRanges(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	tests := []RequestListQuery{
		{Status: "UNKNOWN", Page: 1, PageSize: 20},
		{Venue: "HYBRID", Page: 1, PageSize: 20},
		{Urgency: "CRITICAL", Page: 1, PageSize: 20},
		{PushStatus: "FAILED", Page: 1, PageSize: 20},
		{SortBy: "tenant_id", Page: 1, PageSize: 20},
		{SortOrder: "sideways", Page: 1, PageSize: 20},
		{CreatedFrom: &now, CreatedTo: &now, Page: 1, PageSize: 20},
	}
	for _, query := range tests {
		if _, err := prepareRequestListQuery(query); !errors.Is(err, ErrInvalidFilter) {
			t.Fatalf("query=%+v error=%v, want ErrInvalidFilter", query, err)
		}
	}
}

func TestCRMSuperAdminCanOpenPresaleDetailAndWorklogs(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{request: requestFixture(), worklogs: []Worklog{}}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	actor := managerActor("crm_super_admin")
	if _, err := service.RequestDetail(context.Background(), actor, repo.request.ID); err != nil {
		t.Fatalf("RequestDetail() error=%v, want crm super admin to read tenant presale", err)
	}
	if _, err := service.Worklogs(context.Background(), actor, repo.request.ID); err != nil {
		t.Fatalf("Worklogs() error=%v, want crm super admin to read tenant presale worklogs", err)
	}
}

func TestCRMSuperAdminCanApproveAnyConfiguredNode(t *testing.T) {
	actor := managerActor("crm_super_admin")
	if !approvalNodeRoleAllowed(actor, 1) || !approvalNodeRoleAllowed(actor, 2) {
		t.Fatal("crm super admin should be allowed to approve and reject every approval node")
	}
	nodes, err := json.Marshal([]ApprovalNode{
		{Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{Type: ApprovalNodeApproval, RoleCode: "team_lead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := &ApprovalInstance{NodesJSON: nodes}
	if !approvalNodeRoleAllowedForInstance(actor, 1, instance) || !approvalNodeRoleAllowedForInstance(actor, 2, instance) {
		t.Fatal("crm super admin should be allowed to process every configured approval node")
	}
}

func TestBoardUsesSharedScopeAndFiltersWithBoundedStatusLanes(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	actor := actorWithRoles("sales-1", "person-1", "sales")
	board, err := service.Board(context.Background(), actor, RequestListQuery{RequestNo: "TS2026"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Columns) != len(requestStatuses) || len(repo.receivedQueries) != len(requestStatuses) || board.ColumnLimit != 3 {
		t.Fatalf("board=%+v queries=%d", board, len(repo.receivedQueries))
	}
	for index, query := range repo.receivedQueries {
		if query.RequestNo != "TS2026" || query.Status != requestStatuses[index] || query.Page != 1 || query.PageSize != 3 {
			t.Fatalf("query[%d]=%+v", index, query)
		}
	}
	if repo.receivedScope.All || repo.receivedScope.ApplicantID != actor.UserID || repo.receivedScope.AssigneeID != actor.PersonID {
		t.Fatalf("scope=%+v", repo.receivedScope)
	}
}

func TestBoardStatusFilterDoesNotQueryOtherLanes(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	board, err := service.Board(context.Background(), managerActor("team_lead"), RequestListQuery{Status: StatusExecuting}, 10)
	if err != nil || len(repo.receivedQueries) != 1 || repo.receivedQueries[0].Status != StatusExecuting {
		t.Fatalf("board=%+v queries=%+v error=%v", board, repo.receivedQueries, err)
	}
	if len(board.Columns) != len(requestStatuses) {
		t.Fatalf("columns=%d", len(board.Columns))
	}
}

func TestFilterOptionsUsesAuthenticatedScopeAndSharedFilters(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{filterOptions: RequestFilterOptions{Applicants: []FilterOption{{Value: "sales-1", Label: "Sales"}}}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	actor := actorWithRoles("sales-1", "person-1", "sales")
	options, err := service.FilterOptions(context.Background(), actor, RequestListQuery{Venue: VenueRemote})
	if err != nil || len(options.Applicants) != 1 {
		t.Fatalf("options=%+v error=%v", options, err)
	}
	if len(repo.receivedQueries) != 1 || repo.receivedQueries[0].Venue != VenueRemote || repo.receivedScope.All {
		t.Fatalf("queries=%+v scope=%+v", repo.receivedQueries, repo.receivedScope)
	}
}

func TestListRequestsEnrichesEmptyApplicantSnapshotFromPlatformDirectory(t *testing.T) {
	repo := &queryRepository{}
	directory := &pagedOwnerDirectoryStub{users: []ownerdirectory.User{{ID: "sales-1", DisplayName: "测试销售"}}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).UseOwnerDirectory(directory)
	page, err := service.ListRequests(context.Background(), actorWithRoles("sales-1", "sales-1", "sales"), RequestListQuery{Page: 1, PageSize: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].ApplicantName != "测试销售" {
		t.Fatalf("page=%+v error=%v", page, err)
	}
}

func TestAuditorListAndDetailUseSameAllReadScope(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{request: requestFixture()}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	actor := managerActor("auditor")
	if _, err := service.ListRequests(context.Background(), actor, RequestListQuery{Page: 1, PageSize: 20}); err != nil || !repo.receivedScope.All {
		t.Fatalf("list scope=%+v error=%v", repo.receivedScope, err)
	}
	if _, err := service.RequestDetail(context.Background(), actor, repo.request.ID); err != nil {
		t.Fatalf("auditor detail error=%v", err)
	}
}

func TestRequestDetailBlocksTenantLocalIDORBeforeChildQueries(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{request: requestFixture(), assignments: []Assignment{{AssigneeID: "other-person", IsCurrent: true}}}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	_, err := service.RequestDetail(context.Background(), readableActor("unrelated-sales", "unrelated-person"), repo.request.ID)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("RequestDetail() error=%v, want ErrForbidden", err)
	}
	if repo.aggregateCalls != 0 {
		t.Fatalf("aggregate queried before scope was authorized: %d calls", repo.aggregateCalls)
	}
}

func TestWorklogsBlocksTenantLocalIDORBeforeListingChildren(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{request: requestFixture(), assignments: []Assignment{{AssigneeID: "other-person", IsCurrent: true}}}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	_, err := service.Worklogs(context.Background(), readableActor("unrelated-sales", "unrelated-person"), repo.request.ID)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Worklogs() error=%v, want ErrForbidden", err)
	}
	if repo.worklogListCalls != 0 {
		t.Fatalf("worklogs queried before scope was authorized: %d calls", repo.worklogListCalls)
	}
}

func TestHistoricalAssigneeCanReadWorklogsWithoutCRMRoleInferenceAndDTOHidesInternalFields(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{
		request:     requestFixture(),
		assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: false}},
		worklogs: []Worklog{{
			BaseModel: BaseModel{ID: 9}, RequestID: 7, WorklogNo: "WL1", PersonID: "person-1",
			IdempotencyKey: "private-idempotency", RequestHash: "private-hash",
		}},
	}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	values, err := service.Worklogs(context.Background(), readableActor("other-user", "person-1"), repo.request.ID)
	if err != nil {
		t.Fatalf("Worklogs() error: %v", err)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"private-idempotency", "private-hash", "idempotency_key", "request_hash"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("worklog query leaked %q: %s", forbidden, text)
		}
	}
}

func TestOpportunitySummaryDoesNotRequirePresaleReadButDetailFlagDoes(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{opportunityPage: RequestListPage{Items: []RequestListItem{{ID: 7, ApplicantID: "sales-owner"}}, Total: 1}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	actor := actorWithRoles("sales-owner", "person-1", "sales")
	delete(actor.Permissions, "presale.read")
	page, err := service.ListForOpportunity(context.Background(), actor, 99, 1, 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].CanViewDetail {
		t.Fatalf("summary=%+v error=%v", page, err)
	}
	if page.Items[0].LatestProgress != "latest progress" {
		t.Fatalf("summary did not use opportunity-scoped source: %+v", page.Items[0])
	}
}

func TestOpportunitySummaryUsesTheSameRoleScopeAsPresaleDetails(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{
		opportunityPage: RequestListPage{Items: []RequestListItem{{ID: 7, ApplicantID: "other-sales"}, {ID: 8, ApplicantID: "sales-owner"}}, Total: 2},
		historical:      map[uint64]bool{7: true},
	}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	salesPage, err := service.ListForOpportunity(context.Background(), actorWithRoles("sales-owner", "person-1", "sales"), 99, 1, 20)
	if err != nil || len(salesPage.Items) != 2 || !salesPage.Items[0].CanViewDetail || !salesPage.Items[1].CanViewDetail {
		t.Fatalf("sales summary=%+v error=%v", salesPage, err)
	}
	technicianPage, err := service.ListForOpportunity(context.Background(), actorWithRoles("tech-owner", "person-1", "technician"), 99, 1, 20)
	if err != nil || len(technicianPage.Items) != 2 || !technicianPage.Items[0].CanViewDetail || technicianPage.Items[1].CanViewDetail {
		t.Fatalf("technician summary=%+v error=%v", technicianPage, err)
	}
	assignedPage, err := service.ListForOpportunity(context.Background(), readableActor("other-user", "person-1"), 99, 1, 20)
	if err != nil || len(assignedPage.Items) != 2 || !assignedPage.Items[0].CanViewDetail || assignedPage.Items[1].CanViewDetail {
		t.Fatalf("assigned summary=%+v error=%v", assignedPage, err)
	}
}

func TestEveryPMSAssignmentRoleCanReadWithoutMatchingCRMOIDCRole(t *testing.T) {
	t.Parallel()
	for _, assignmentRole := range []string{"technical_director", "team_lead", "project_manager", "technician"} {
		assignmentRole := assignmentRole
		t.Run(assignmentRole, func(t *testing.T) {
			t.Parallel()
			repo := &queryRepository{
				request: requestFixture(),
				assignments: []Assignment{{
					AssigneeID: "person-1", AssigneeRole: assignmentRole, IsCurrent: assignmentRole != "project_manager",
				}},
			}
			service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
			actor := readableActor("non-technician-user", "person-1")
			if _, err := service.RequestDetail(context.Background(), actor, repo.request.ID); err != nil {
				t.Fatalf("RequestDetail() role=%s error=%v", assignmentRole, err)
			}
			if _, err := service.ListRequests(context.Background(), actor, RequestListQuery{Page: 1, PageSize: 20}); err != nil {
				t.Fatalf("ListRequests() role=%s error=%v", assignmentRole, err)
			}
			if repo.receivedScope.AssigneeID != actor.PersonID || repo.receivedScope.All || repo.receivedScope.ApplicantID != "" {
				t.Fatalf("scope=%+v", repo.receivedScope)
			}
		})
	}
}

func TestUnassignedPersonCannotReadTaskWithoutAnotherAuthorizedRelation(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{request: requestFixture(), assignments: []Assignment{{AssigneeID: "other-person", IsCurrent: true}}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	if _, err := service.RequestDetail(context.Background(), readableActor("other-user", "person-1"), repo.request.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("RequestDetail() error=%v, want ErrForbidden", err)
	}
}

func TestRequestDetailExposesConfiguredPersonAssignment(t *testing.T) {
	t.Parallel()
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "people", Type: ApprovalNodePerson, RoleCode: "technical_director"},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &queryRepository{
		request:  requestFixture(),
		approval: &ApprovalInstance{NodesJSON: nodes},
	}
	actor := managerActor("technical_director")
	actor.Permissions["presale.assign"] = true
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	value, err := service.RequestDetail(context.Background(), actor, repo.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !timelineContains(value.AvailableActions, "ASSIGN") {
		t.Fatalf("available actions=%v, want configured technical-director assignment", value.AvailableActions)
	}
}

func TestAlertAggregateHasSingleTenantPredicate(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "func (r *GORMRepository) AlertAggregate")
	end := strings.Index(text[start:], "func (r *GORMRepository) ListCurrentAssignments")
	if start < 0 || end < 0 {
		t.Fatal("AlertAggregate source boundary missing")
	}
	functionSource := text[start : start+end]
	if count := strings.Count(functionSource, "WHERE a.tenant_id=?"); count != 1 {
		t.Fatalf("AlertAggregate must contain one tenant WHERE, got %d", count)
	}
	if strings.Contains(functionSource, "WHERE a.tenant_id=? AND a.request_id=? AND a.status='UNREAD' AND a.deleted_at IS NULL\n\t\tWHERE") {
		t.Fatal("AlertAggregate contains a duplicate WHERE clause")
	}
}

func readableActor(userID, personID string) Actor {
	return Actor{
		TenantID: "tenant-1", UserID: userID, PersonID: personID,
		Permissions: map[string]bool{"presale.read": true}, Roles: map[string]bool{},
	}
}

func actorWithRoles(userID, personID string, roles ...string) Actor {
	actor := readableActor(userID, personID)
	for _, role := range roles {
		actor.Roles[role] = true
	}
	return actor
}

func managerActor(role string) Actor {
	actor := readableActor("manager-1", "manager-person")
	actor.Roles[role] = true
	return actor
}

func requestFixture() *PresaleRequest {
	return &PresaleRequest{
		BaseModel:   BaseModel{ID: 7, TenantID: "tenant-1", Version: 1},
		ApplicantID: "sales-owner", Status: StatusExecuting, ExpectedEnd: time.Now().Add(time.Hour),
	}
}
