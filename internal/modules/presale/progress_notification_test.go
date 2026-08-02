package presale

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestAddProgressWritesOccurrenceTimePersonalNotificationEvidence(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	repo := &progressRepository{
		request: &PresaleRequest{
			BaseModel:   BaseModel{ID: 7, TenantID: "tenant-a", Version: 3},
			ApplicantID: "sales-user", Status: StatusExecuting,
		},
		assignments: []Assignment{
			{BaseModel: BaseModel{ID: 10}, AssigneeID: "author-person", IsCurrent: true},
			{BaseModel: BaseModel{ID: 11}, AssigneeID: "peer-person", IsCurrent: true},
			// Corrupt duplicate current rows must not produce duplicate messages.
			{BaseModel: BaseModel{ID: 12}, AssigneeID: "peer-person", IsCurrent: true},
		},
		byKey: map[string]*ProgressLog{},
	}
	service := NewService(repo, nil, nil, fixedClock{at: now}, fixedIDs{})
	actor := Actor{TenantID: "tenant-a", UserID: "author-user", PersonID: "author-person", RequestID: "trace-a", Permissions: map[string]bool{"presale.progress": true}}
	created, err := service.AddProgress(context.Background(), actor, 7, "progress-key", AddProgressInput{Content: "完成环境检查", Version: 3})
	if err != nil || created == nil {
		t.Fatalf("progress=%+v error=%v", created, err)
	}
	if len(repo.notificationEvents) != 2 || len(repo.outbox) != 2 {
		t.Fatalf("events=%#v outbox=%#v", repo.notificationEvents, repo.outbox)
	}
	applicant, peer := repo.notificationEvents[0], repo.notificationEvents[1]
	if applicant.RecipientNamespace != ProgressRecipientUser || applicant.RecipientKind != ProgressRecipientApplicant || applicant.RecipientID != "sales-user" || applicant.AssignmentID != 0 {
		t.Fatalf("applicant evidence=%#v", applicant)
	}
	if peer.RecipientNamespace != ProgressRecipientPerson || peer.RecipientKind != ProgressRecipientAssignee || peer.RecipientID != "peer-person" || peer.AssignmentID != 11 {
		t.Fatalf("peer evidence=%#v", peer)
	}
	for index, event := range repo.notificationEvents {
		if repo.outbox[index].EventID != event.EventID || repo.outbox[index].EventType != ProgressNotificationOutboxEventType || repo.outbox[index].AggregateID == "" {
			t.Fatalf("event/outbox mismatch at %d", index)
		}
		var payload map[string]any
		if err = json.Unmarshal(repo.outbox[index].Payload, &payload); err != nil || len(payload) != 1 || payload["progress_notification_event_id"] == nil {
			t.Fatalf("payload=%s error=%v", repo.outbox[index].Payload, err)
		}
	}
}

func TestProgressNotificationExclusionNeverCrossesRecipientNamespaces(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		applicantUser string
		wantEvents    int
	}{
		{name: "same user applicant is excluded", applicantUser: "same-value", wantEvents: 0},
		// Equality with author person_id is not proof that a USER recipient is
		// the author, because user and person namespaces are independent.
		{name: "same bytes in different namespace are retained", applicantUser: "author-person", wantEvents: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &progressRepository{
				request:     &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: "tenant-a", Version: 3}, ApplicantID: test.applicantUser, Status: StatusExecuting},
				assignments: []Assignment{{BaseModel: BaseModel{ID: 10}, AssigneeID: "author-person", IsCurrent: true}}, byKey: map[string]*ProgressLog{},
			}
			service := NewService(repo, nil, nil, fixedClock{at: now}, fixedIDs{})
			actor := Actor{TenantID: "tenant-a", UserID: "same-value", PersonID: "author-person", Permissions: map[string]bool{"presale.progress": true}}
			if _, err := service.AddProgress(context.Background(), actor, 7, "key", AddProgressInput{Content: "进度", Version: 3}); err != nil {
				t.Fatal(err)
			}
			if len(repo.notificationEvents) != test.wantEvents {
				t.Fatalf("events=%#v", repo.notificationEvents)
			}
		})
	}
}

func TestExactProgressReplayDoesNotDuplicateNotificationEvidence(t *testing.T) {
	repo := &progressRepository{
		request:     &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: "tenant-a", Version: 3}, ApplicantID: "sales", Status: StatusExecuting},
		assignments: []Assignment{{BaseModel: BaseModel{ID: 10}, AssigneeID: "author-person", IsCurrent: true}}, byKey: map[string]*ProgressLog{},
	}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	actor := Actor{TenantID: "tenant-a", UserID: "author-user", PersonID: "author-person", Permissions: map[string]bool{"presale.progress": true}}
	input := AddProgressInput{Content: "进度", Version: 3}
	if _, err := service.AddProgress(context.Background(), actor, 7, "same", input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddProgress(context.Background(), actor, 7, "same", input); err != nil {
		t.Fatal(err)
	}
	if len(repo.notificationEvents) != 1 || len(repo.outbox) != 1 {
		t.Fatalf("events=%d outbox=%d", len(repo.notificationEvents), len(repo.outbox))
	}
}
