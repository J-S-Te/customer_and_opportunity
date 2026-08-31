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

type timelineRepository struct {
	Repository
	request       *PresaleRequest
	assignments   []Assignment
	records       []TimelineRecord
	timelineCalls int
	received      *TimelineCursor
}

func TestTimelineEnrichesHistoricalActorNamesFromPlatformDirectory(t *testing.T) {
	repo := &timelineRepository{request: requestFixture(), records: []TimelineRecord{{
		SourceID: 1, TypePriority: 10, EventType: "STATUS_CHANGED", OccurredAt: time.Now().UTC(), ActorID: "lead-1",
	}}}
	directory := &pagedOwnerDirectoryStub{users: []ownerdirectory.User{{ID: "lead-1", DisplayName: "测试团队负责人"}}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).
		UseOwnerDirectory(directory).
		UseTimelineCursorKey([]byte("01234567890123456789012345678901"))
	page, err := service.Timeline(context.Background(), managerActor("team_lead"), repo.request.ID, "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].ActorName != "测试团队负责人" {
		t.Fatalf("page=%+v error=%v", page, err)
	}
}

func (r *timelineRepository) FindRequest(_ context.Context, tenant string, id uint64) (*PresaleRequest, error) {
	if r.request == nil || r.request.TenantID != tenant || r.request.ID != id {
		return nil, ErrNotFound
	}
	return r.request, nil
}

func (r *timelineRepository) ListAssignments(_ context.Context, tenant string, id uint64) ([]Assignment, error) {
	if r.request == nil || r.request.TenantID != tenant || r.request.ID != id {
		return nil, ErrNotFound
	}
	return r.assignments, nil
}

func (r *timelineRepository) ListTimeline(_ context.Context, tenant string, id uint64, cursor *TimelineCursor, limit int) ([]TimelineRecord, error) {
	r.timelineCalls++
	if r.request == nil || r.request.TenantID != tenant || r.request.ID != id {
		return nil, ErrNotFound
	}
	r.received = cursor
	if len(r.records) > limit {
		return r.records[:limit], nil
	}
	return r.records, nil
}

func TestTimelineBlocksIDORBeforeReadingEvents(t *testing.T) {
	t.Parallel()
	repo := &timelineRepository{request: requestFixture(), assignments: []Assignment{{AssigneeID: "someone-else"}}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).UseTimelineCursorKey([]byte("01234567890123456789012345678901"))
	_, err := service.Timeline(context.Background(), readableActor("unrelated", "unrelated-person"), repo.request.ID, "", 20)
	if !errors.Is(err, ErrForbidden) || repo.timelineCalls != 0 {
		t.Fatalf("Timeline() error=%v timeline calls=%d", err, repo.timelineCalls)
	}
}

func TestTimelineUsesSignedScopedKeysetAndMinimalDTO(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 9, 0, 0, 123000000, time.UTC)
	repo := &timelineRepository{request: requestFixture(), records: []TimelineRecord{
		{SourceID: 9, TypePriority: 50, EventType: "WORKLOG_ADDED", OccurredAt: now, ActorID: "worker", ActorName: "Worker", WorkHours: "8.00", WorkContent: "POC_DEMO"},
		{SourceID: 8, TypePriority: 40, EventType: "PROGRESS_ADDED", OccurredAt: now.Add(-time.Second), ActorID: "worker", Content: "safe summary", LinkURL: "https://example.invalid"},
		{SourceID: 7, TypePriority: 10, EventType: "STATUS_CHANGED", OccurredAt: now.Add(-2 * time.Second), FromStatus: StatusExecuting, ToStatus: StatusCompleted},
	}}
	key := []byte("01234567890123456789012345678901")
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).UseTimelineCursorKey(key)
	actor := managerActor("team_lead")
	first, err := service.Timeline(context.Background(), actor, repo.request.ID, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page=%+v error=%v", first, err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"approval comment", "contact_phone", "work_site_address", "remark", "idempotency"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("timeline DTO leaked %q: %s", forbidden, encoded)
		}
	}
	second, err := service.Timeline(context.Background(), actor, repo.request.ID, first.NextCursor, 2)
	if err != nil || repo.received == nil || !repo.received.OccurredAt.Equal(repo.records[1].OccurredAt) || repo.received.SourceID != 8 {
		t.Fatalf("cursor=%+v second=%+v error=%v", repo.received, second, err)
	}
	otherRepo := &timelineRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 8, TenantID: repo.request.TenantID}, ApplicantID: actor.UserID}}
	otherService := NewService(otherRepo, nil, nil, fixedClock{}, fixedIDs{}).UseTimelineCursorKey(key)
	if _, err = otherService.Timeline(context.Background(), actor, otherRepo.request.ID, first.NextCursor, 2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor reused for a different request error=%v", err)
	}
	forged := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if _, err = service.Timeline(context.Background(), actor, repo.request.ID, forged, 2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("forged cursor error=%v", err)
	}
}

func TestAvailableActionsFailsClosedWithoutApprovalTaskResolutionContract(t *testing.T) {
	t.Parallel()
	salesDirector := managerActor("sales_director")
	salesDirector.Permissions["presale.approve"] = true
	teamLead := managerActor("team_lead")
	teamLead.Permissions["presale.approve"] = true
	if got := localAvailableActions(salesDirector, StatusPendingApproval, "applicant", nil); timelineContains(got, "APPROVE") {
		t.Fatalf("node-2 approval exposed to sales director: %v", got)
	}
	if got := localAvailableActions(teamLead, StatusPendingApproval, "applicant", nil); timelineContains(got, "APPROVE") {
		t.Fatalf("node-1 approval exposed to team lead: %v", got)
	}
	if got := localAvailableActions(teamLead, StatusPendingApproval, "applicant", nil); timelineContains(got, "APPROVE") || timelineContains(got, "REJECT") {
		t.Fatalf("approval action exposed without a resolvable engine task: %v", got)
	}
}

func TestLegacyPersonAssignmentAllowsTechnicalDirector(t *testing.T) {
	t.Parallel()
	actor := managerActor("technical_director")
	actor.Permissions["presale.assign"] = true
	if got := localAvailableActions(actor, StatusApprovedPendingAssignment, "applicant", nil); !timelineContains(got, "ASSIGN") {
		t.Fatalf("technical director actions=%v, want ASSIGN", got)
	}
}

type recordingApprovalTaskResolver struct {
	query ApprovalTaskQuery
	task  ApprovalTask
	err   error
}

func (r *recordingApprovalTaskResolver) ResolveCurrentTask(_ context.Context, query ApprovalTaskQuery) (ApprovalTask, error) {
	r.query = query
	return r.task, r.err
}

func TestAvailableActionsPublishesApprovalOnlyAfterAuthoritativeTaskResolution(t *testing.T) {
	actor := managerActor("team_lead")
	actor.UserID = "lead-1"
	actor.Permissions["presale.approve"] = true
	repo := &queryRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: actor.TenantID, Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 2},
		approval: &ApprovalInstance{EngineInstanceID: "instance-7", Status: "PENDING", CurrentNode: 2},
	}
	resolver := &recordingApprovalTaskResolver{task: ApprovalTask{EngineTaskID: "task-7", EngineInstanceID: "instance-7", Node: 2, ApproverID: actor.UserID}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).UseApprovalTaskResolver(resolver)
	value, err := service.AvailableActions(context.Background(), actor, 7)
	if err != nil || !timelineContains(value.Actions, "APPROVE") || !timelineContains(value.Actions, "REJECT") {
		t.Fatalf("actions=%+v error=%v", value, err)
	}
	if resolver.query.TenantID != actor.TenantID || resolver.query.EngineInstanceID != "instance-7" || resolver.query.Node != 2 || resolver.query.ApproverID != actor.UserID {
		t.Fatalf("resolver query=%+v", resolver.query)
	}
	repo.approval.PendingTaskID, repo.approval.PendingApprover, repo.approval.PendingAction = "task-7", actor.UserID, "PASS"
	value, err = service.AvailableActions(context.Background(), actor, 7)
	if err != nil || timelineContains(value.Actions, "APPROVE") || timelineContains(value.Actions, "REJECT") {
		t.Fatalf("in-flight approval actions=%+v error=%v", value, err)
	}
	repo.approval.PendingTaskID, repo.approval.PendingApprover, repo.approval.PendingAction = "", "", ""
	resolver.task.ApproverID = "another-user"
	value, err = service.AvailableActions(context.Background(), actor, 7)
	if err != nil || timelineContains(value.Actions, "APPROVE") || timelineContains(value.Actions, "REJECT") {
		t.Fatalf("mismatched task actions=%+v error=%v", value, err)
	}
}

func TestAvailableActionsUsesConfiguredApprovalNodeRole(t *testing.T) {
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "sales", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "technical", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := &PresaleRequest{BaseModel: BaseModel{ID: 8, TenantID: "tenant-1", Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 2}
	instance := &ApprovalInstance{EngineInstanceID: "instance-8", Status: "PENDING", CurrentNode: 2, NodesJSON: nodes}
	resolver := &recordingApprovalTaskResolver{}
	repo := &queryRepository{request: request, approval: instance}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).UseApprovalTaskResolver(resolver)

	teamLead := managerActor("team_lead")
	teamLead.UserID = "team-lead"
	teamLead.Permissions["presale.approve"] = true
	resolver.task = ApprovalTask{EngineTaskID: "wrong-role-task", EngineInstanceID: instance.EngineInstanceID, Node: 2, ApproverID: teamLead.UserID}
	value, err := service.AvailableActions(context.Background(), teamLead, request.ID)
	if err != nil || timelineContains(value.Actions, "APPROVE") || timelineContains(value.Actions, "REJECT") {
		t.Fatalf("team lead received technical-director node actions=%v error=%v", value.Actions, err)
	}

	technicalDirector := managerActor("technical_director")
	technicalDirector.UserID = "technical-director"
	technicalDirector.Permissions["presale.approve"] = true
	resolver.task = ApprovalTask{EngineTaskID: "technical-task", EngineInstanceID: instance.EngineInstanceID, Node: 2, ApproverID: technicalDirector.UserID}
	value, err = service.AvailableActions(context.Background(), technicalDirector, request.ID)
	if err != nil || !timelineContains(value.Actions, "APPROVE") || !timelineContains(value.Actions, "REJECT") {
		t.Fatalf("technical director actions=%v error=%v", value.Actions, err)
	}
}

func TestTimelineMigrationAddsAndRemovesCoveringIndexes(t *testing.T) {
	t.Parallel()
	up, err := os.ReadFile("../../../migrations/000035_presale_timeline_indexes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000035_presale_timeline_indexes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"idx_presale_assignment_request", "idx_presale_assignment_end_timeline", "idx_presale_worklog_timeline", "No row backfill is required"} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	for _, required := range []string{"DROP INDEX idx_presale_assignment_end_timeline", "DROP INDEX idx_presale_worklog_timeline"} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("down migration missing %q", required)
		}
	}
}

func TestPresaleRoutesProtectTimelineWithReadPermission(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, route := range []string{"/requests/:id/timeline", "/requests/:id/available-actions", "/board", `presale.GET("/filter-options"`} {
		lineFound := false
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, route) {
				lineFound = true
				if !strings.Contains(line, `RequirePermission("presale.read")`) {
					t.Fatalf("route %s is not protected by presale.read: %s", route, line)
				}
			}
		}
		if !lineFound {
			t.Fatalf("route %s missing", route)
		}
	}
}

func timelineContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
