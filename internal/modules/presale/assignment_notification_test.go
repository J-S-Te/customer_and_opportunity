package presale

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

type assignmentOwnerCatalog struct {
	code  ownerdirectory.UnavailableCatalog
	users map[string]ownerdirectory.User
}

func (c assignmentOwnerCatalog) List(ctx context.Context, query ownerdirectory.Query) (ownerdirectory.Page, error) {
	return c.code.List(ctx, query)
}

func (c assignmentOwnerCatalog) Validate(ctx context.Context, userID, organizationID string) error {
	return c.code.Validate(ctx, userID, organizationID)
}

func (c assignmentOwnerCatalog) Resolve(_ context.Context, userIDs []string) (map[string]ownerdirectory.User, error) {
	values := make(map[string]ownerdirectory.User, len(userIDs))
	for _, userID := range userIDs {
		if user, ok := c.users[userID]; ok {
			values[userID] = user
		}
	}
	return values, nil
}

type assignmentNotificationRepository struct {
	*mutationRepository
	events []AssignmentEvent
	outbox []OutboxEvent
}

func (r *assignmentNotificationRepository) CreateAssignment(_ context.Context, value *Assignment) error {
	value.ID = uint64(100 + len(r.assignments) + 1)
	r.assignmentAdds++
	r.assignments = append(r.assignments, *value)
	return nil
}
func (r *assignmentNotificationRepository) EndAssignment(_ context.Context, _ string, id, _ uint64, _ string, at time.Time) error {
	for index := range r.assignments {
		if r.assignments[index].ID == id {
			r.assignments[index].IsCurrent = false
			r.assignments[index].EndedAt = &at
		}
	}
	return nil
}
func (r *assignmentNotificationRepository) CreateAssignmentEvent(_ context.Context, value *AssignmentEvent) error {
	value.ID = uint64(len(r.events) + 1)
	r.events = append(r.events, *value)
	return nil
}
func (r *assignmentNotificationRepository) CreateOutbox(_ context.Context, value *OutboxEvent) error {
	r.outbox = append(r.outbox, *value)
	return nil
}

func TestReplaceAssignmentsWritesEvidenceAndOutboxForAddedAndRemoved(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	actor := Actor{TenantID: "tenant-a", UserID: "lead", RequestID: "request-trace", Roles: map[string]bool{"team_lead": true}, Permissions: map[string]bool{"presale.assign": true}}
	base := &mutationRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS9", Status: StatusExecuting}, assignments: []Assignment{{BaseModel: BaseModel{ID: 20, TenantID: actor.TenantID, Version: 1}, RequestID: 9, AssigneeID: "old", AssigneeNameSnapshot: "旧人员", AssigneeRole: "implementation_engineer", AssignedAt: now.Add(-time.Hour), IsCurrent: true}}, engineers: []Engineer{{PersonID: "new", PersonName: "新人员", Role: "project_manager", ValidFlag: true}}, replays: map[string]*MutationReplay{}}
	repo := &assignmentNotificationRepository{mutationRepository: base}
	service := NewService(repo, nil, nil, fixedClock{at: now}, fixedIDs{}).UseOwnerDirectory(assignmentOwnerCatalog{users: map[string]ownerdirectory.User{
		"new": {ID: "new", DisplayName: "权威姓名", Organizations: []ownerdirectory.Organization{{ID: "org-1", Name: "技术中心", IsPrimary: true}}},
	}})
	// 浏览器只选择人员；组织归属必须由服务端从基础平台权威目录推导，不能要求
	// 前端携带或手填内部 organization_id。
	_, err := service.ReplaceAssignments(context.Background(), actor, 9, "key", ReplaceAssignmentsInput{Assignees: []AssignmentTarget{{PersonID: "new", PersonName: "伪造姓名", Department: "伪造部门", Role: "project_manager"}}, ChangeReason: "项目阶段变化", Version: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.events) != 2 || len(repo.outbox) != 2 {
		t.Fatalf("events=%d outbox=%d", len(repo.events), len(repo.outbox))
	}
	if repo.events[0].EventType != AssignmentEventRemoved || repo.events[0].RecipientPersonID != "old" || repo.events[1].EventType != AssignmentEventAdded || repo.events[1].RecipientPersonID != "new" {
		t.Fatalf("events=%#v", repo.events)
	}
	added := repo.assignments[len(repo.assignments)-1]
	if added.AssigneeNameSnapshot != "权威姓名" || added.AssigneeDepartmentSnapshot != "技术中心" {
		t.Fatalf("assignment did not use authoritative directory snapshot: %#v", added)
	}
	for index := range repo.events {
		if repo.events[index].EventID != repo.outbox[index].EventID || repo.outbox[index].EventType != assignmentNotificationEventType {
			t.Fatalf("event/outbox mismatch")
		}
	}
	addedEvent := repo.events[1]
	addedOutbox := repo.outbox[1]
	if addedEvent.RecipientPersonID != "new" || addedEvent.AssignmentID != added.ID || addedEvent.EventType != AssignmentEventAdded {
		t.Fatalf("added assignee notification evidence=%#v", addedEvent)
	}
	if addedEvent.EventID != AssignmentNotificationEventID(actor.TenantID, base.request.ID, added.ID, AssignmentEventAdded) {
		t.Fatalf("added assignee notification event id=%q", addedEvent.EventID)
	}
	var payload struct {
		AssignmentEventID uint64 `json:"assignment_event_id"`
	}
	if err := json.Unmarshal(addedOutbox.Payload, &payload); err != nil {
		t.Fatalf("decode assignment notification payload: %v", err)
	}
	if payload.AssignmentEventID != addedEvent.ID || addedOutbox.AggregateType != "presale_assignment_event" ||
		addedOutbox.AggregateID != strconv.FormatUint(addedEvent.ID, 10) || addedOutbox.Status != "PENDING" {
		t.Fatalf("added assignee notification outbox=%#v payload=%#v", addedOutbox, payload)
	}
}

func TestExactAssignmentReplayDoesNotDuplicateNotifications(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	actor := Actor{TenantID: "tenant-a", UserID: "lead", Roles: map[string]bool{"team_lead": true}, Permissions: map[string]bool{"presale.assign": true}}
	base := &mutationRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, Status: StatusApprovedPendingAssignment}, engineers: []Engineer{{PersonID: "new", PersonName: "新人员", Role: "project_manager", ValidFlag: true}}, replays: map[string]*MutationReplay{}}
	repo := &assignmentNotificationRepository{mutationRepository: base}
	service := NewService(repo, nil, nil, fixedClock{at: now}, fixedIDs{})
	input := ReplaceAssignmentsInput{Assignees: []AssignmentTarget{{PersonID: "new", Role: "project_manager"}}, ChangeReason: "首次指派", Version: 3}
	if _, err := service.ReplaceAssignments(context.Background(), actor, 9, "same-key", input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceAssignments(context.Background(), actor, 9, "same-key", input); err != nil {
		t.Fatal(err)
	}
	if len(repo.events) != 1 || len(repo.outbox) != 1 {
		t.Fatalf("events=%d outbox=%d", len(repo.events), len(repo.outbox))
	}
}
