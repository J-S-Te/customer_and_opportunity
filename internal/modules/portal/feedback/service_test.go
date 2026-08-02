package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type memoryRepo struct {
	value                      *Feedback
	messages                   []Message
	logs                       []StatusLog
	outbox                     []Outbox
	listCustomer, listOperator int
}

func TestCustomerTimelineJSONDoesNotLeakPersistenceOrOperatorIdentity(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)
	value := &Feedback{ActorModel: ActorModel{ID: 99, TenantID: "secret-tenant", CreatedBy: "secret-creator", UpdatedBy: "secret-updater", Version: 7}, PublicID: "feedback-public", FeedbackNo: "FB-1", CustomerID: 55, AccountID: "secret-sub", ExpectedContactCipher: []byte("secret-cipher"), CreateIdempotencyKey: "secret-key", CreateRequestHash: "secret-hash", Status: StatusSubmitted, SubmittedAt: now, FirstResponseDueAt: now.Add(time.Hour)}
	view := customerTimeline(value, []Message{{ID: 8, TenantID: "secret-tenant", FeedbackID: 99, SenderType: "OPERATOR", SenderID: "secret-operator", Content: "visible response", Visibility: "CUSTOMER", IdempotencyKey: "message-secret", RequestHash: "message-hash", CreatedAt: now}}, []StatusLog{{ID: 7, TenantID: "secret-tenant", FeedbackID: 99, ActorID: "secret-operator", RequestID: "secret-request", ToStatus: StatusSubmitted, ActorType: "OPERATOR", OccurredAt: now}})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"secret-tenant", "secret-creator", "secret-updater", "secret-sub", "secret-cipher", "secret-key", "secret-hash", "secret-operator", "secret-request", "message-secret", "message-hash", "customer_id", "account_id", "created_by", "updated_by"} {
		if strings.Contains(text, secret) {
			t.Fatalf("customer view leaked %q: %s", secret, text)
		}
	}
}

func TestFeedbackListRejectsOutOfRangeMachineQueriesBeforeRepository(t *testing.T) {
	repository := &memoryRepo{}
	service := NewService(repository, nil, nil, nil, nil)
	if _, err := service.List(context.Background(), CustomerActor{TenantID: "tenant-a", CustomerID: 7, AccountID: "subject-a"}, ListQuery{Page: maxFeedbackQueryPage + 1, PageSize: 1}); !errors.Is(err, ErrValidation) {
		t.Fatalf("customer list error = %v", err)
	}
	if _, err := service.ListForOperator(context.Background(), OperatorActor{TenantID: "tenant-a", ActorID: "operator-a"}, ListQuery{Page: maxFeedbackQueryPage + 1, PageSize: 1}); !errors.Is(err, ErrValidation) {
		t.Fatalf("operator list error = %v", err)
	}
	if repository.listCustomer != 0 || repository.listOperator != 0 {
		t.Fatalf("invalid query reached repository: customer=%d operator=%d", repository.listCustomer, repository.listOperator)
	}
}

func (r *memoryRepo) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *memoryRepo) Create(_ context.Context, value *Feedback) error {
	value.ID = 1
	r.value = value
	return nil
}
func (r *memoryRepo) FindByCreateKey(_ context.Context, actor CustomerActor, key string) (*Feedback, error) {
	if r.value != nil && r.value.TenantID == actor.TenantID && r.value.CustomerID == actor.CustomerID && r.value.AccountID == actor.AccountID && r.value.CreateIdempotencyKey == key {
		return r.value, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepo) ListCustomer(context.Context, CustomerActor, ListQuery) (pagination.Page[Feedback], error) {
	r.listCustomer++
	return pagination.Page[Feedback]{}, nil
}
func (r *memoryRepo) FindCustomer(_ context.Context, actor CustomerActor, id string) (*Feedback, error) {
	if r.value != nil && r.value.TenantID == actor.TenantID && r.value.CustomerID == actor.CustomerID && r.value.AccountID == actor.AccountID && r.value.PublicID == id {
		return r.value, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepo) FindOperator(_ context.Context, tenant, id string) (*Feedback, error) {
	if r.value != nil && r.value.TenantID == tenant && r.value.PublicID == id {
		return r.value, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepo) ListOperator(context.Context, string, ListQuery) (pagination.Page[Feedback], error) {
	r.listOperator++
	return pagination.Page[Feedback]{}, nil
}
func (r *memoryRepo) FindForUpdate(_ context.Context, tenant string, id uint64) (*Feedback, error) {
	if r.value != nil && r.value.TenantID == tenant && r.value.ID == id {
		return r.value, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepo) Update(_ context.Context, value *Feedback, version uint64, fields map[string]any) error {
	if value.Version != version {
		return errors.New("version")
	}
	if status, ok := fields["status"].(Status); ok {
		value.Status = status
	}
	if at, ok := fields["first_responded_at"].(*time.Time); ok {
		value.FirstRespondedAt = at
	}
	value.Version++
	return nil
}
func (r *memoryRepo) CreateMessage(_ context.Context, m *Message) error {
	m.ID = uint64(len(r.messages) + 1)
	r.messages = append(r.messages, *m)
	return nil
}
func (r *memoryRepo) FindMessageByKey(_ context.Context, tenant string, id uint64, senderType, senderID, key string) (*Message, error) {
	for i := range r.messages {
		m := &r.messages[i]
		if m.TenantID == tenant && m.FeedbackID == id && m.SenderType == senderType && m.SenderID == senderID && m.IdempotencyKey == key {
			return m, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepo) ListCustomerMessages(_ context.Context, tenant string, id uint64) ([]Message, error) {
	var out []Message
	for _, m := range r.messages {
		if m.TenantID == tenant && m.FeedbackID == id && m.Visibility == "CUSTOMER" {
			out = append(out, m)
		}
	}
	return out, nil
}
func (r *memoryRepo) ListStatusLogs(context.Context, string, uint64) ([]StatusLog, error) {
	return r.logs, nil
}
func (r *memoryRepo) FindStatusActionByKey(_ context.Context, tenant, key string) (*StatusLog, error) {
	for i := range r.logs {
		value := &r.logs[i]
		if value.TenantID == tenant && value.IdempotencyKey != nil && *value.IdempotencyKey == key {
			return value, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepo) CreateStatusLog(_ context.Context, l *StatusLog) error {
	r.logs = append(r.logs, *l)
	return nil
}
func (r *memoryRepo) CreateOutbox(_ context.Context, o *Outbox) error {
	r.outbox = append(r.outbox, *o)
	return nil
}

type projectStub struct{ allowed bool }

func (p projectStub) Accessible(context.Context, string, uint64, string) (bool, error) {
	return p.allowed, nil
}

type contactStub struct{}

func (contactStub) Encrypt(_ context.Context, value string) ([]byte, string, error) {
	return []byte("cipher:" + value), "***", nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type ids struct{ n int }

func (i *ids) NewID() string { i.n++; return "0000000000000000000000000000000" + string(rune('0'+i.n)) }

func TestCreateIsScopedIdempotentAndSetsSLA(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	repo := &memoryRepo{}
	service := NewService(repo, projectStub{true}, contactStub{}, fixedClock{now}, &ids{})
	actor := CustomerActor{TenantID: "t1", CustomerID: 2, AccountID: "sub-1"}
	cmd := CreateCommand{Type: "complaint", Title: "issue", Description: "details", ProjectID: "p1", IdempotencyKey: "idem-key-1"}
	first, err := service.Create(context.Background(), actor, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusSubmitted || !first.FirstResponseDueAt.Equal(now.Add(24*time.Hour)) || len(repo.outbox) != 1 {
		t.Fatalf("created=%+v outbox=%d", first, len(repo.outbox))
	}
	second, err := service.Create(context.Background(), actor, cmd)
	if err != nil || second.ID != first.ID || len(repo.outbox) != 1 {
		t.Fatalf("replay=%+v err=%v outbox=%d", second, err, len(repo.outbox))
	}
	_, err = service.Create(context.Background(), actor, CreateCommand{Type: "complaint", Title: "changed", Description: "details", ProjectID: "p1", IdempotencyKey: "idem-key-1"})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestCustomerCannotReadAnotherSubjectFeedback(t *testing.T) {
	repo := &memoryRepo{value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1"}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1"}}
	service := NewService(repo, nil, contactStub{}, fixedClock{}, &ids{})
	_, err := service.Get(context.Background(), CustomerActor{TenantID: "t1", CustomerID: 2, AccountID: "sub-2"}, "feedback-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestFirstVisibleOperatorResponseStopsSLAAndInternalNoteStaysHidden(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)
	repo := &memoryRepo{value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1", Version: 1}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1", Status: StatusAccepted}}
	service := NewService(repo, nil, contactStub{}, fixedClock{now}, &ids{})
	operator := OperatorActor{TenantID: "t1", ActorID: "machine:desk"}
	if _, err := service.Process(context.Background(), operator, "feedback-1", "note", ProcessCommand{Content: "secret note", IdempotencyKey: "note-key-1"}); err != nil {
		t.Fatal(err)
	}
	if repo.value.FirstRespondedAt != nil {
		t.Fatal("internal note stopped SLA")
	}
	if _, err := service.Process(context.Background(), operator, "feedback-1", "respond", ProcessCommand{Content: "we are handling it", IdempotencyKey: "reply-key-1"}); err != nil {
		t.Fatal(err)
	}
	if repo.value.FirstRespondedAt == nil || repo.value.Status != StatusProcessing {
		t.Fatalf("value=%+v", repo.value)
	}
	view, err := service.Get(context.Background(), CustomerActor{TenantID: "t1", CustomerID: 2, AccountID: "sub-1"}, "feedback-1")
	if err != nil || len(view.Messages) != 1 || view.Messages[0].Content != "we are handling it" {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

func TestOperatorStatusActionIdempotencyBindsActorKeyAndPayload(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)
	repo := &memoryRepo{value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1", Version: 1}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1", Status: StatusSubmitted}}
	service := NewService(repo, nil, contactStub{}, fixedClock{now}, &ids{})
	operator := OperatorActor{TenantID: "t1", ActorID: "machine:desk-a"}
	command := ProcessCommand{IdempotencyKey: "accept-key-1"}
	if _, err := service.Process(context.Background(), operator, "feedback-1", "accept", command); err != nil {
		t.Fatal(err)
	}
	logCount := len(repo.logs)
	if _, err := service.Process(context.Background(), operator, "feedback-1", "accept", command); err != nil || len(repo.logs) != logCount {
		t.Fatalf("replay err=%v logs=%d", err, len(repo.logs))
	}
	conflict := command
	conflict.Content = "different"
	if _, err := service.Process(context.Background(), operator, "feedback-1", "accept", conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("payload conflict err=%v", err)
	}
	otherActor := OperatorActor{TenantID: "t1", ActorID: "machine:desk-b"}
	if _, err := service.Process(context.Background(), otherActor, "feedback-1", "accept", command); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("other actor must receive opaque key conflict, err=%v", err)
	}
}

func TestRequestInfoStatusActionReplaysAfterTransition(t *testing.T) {
	now := time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC)
	repo := &memoryRepo{value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1", Version: 1}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1", Status: StatusProcessing, FirstRespondedAt: &now}}
	service := NewService(repo, nil, contactStub{}, fixedClock{now}, &ids{})
	operator := OperatorActor{TenantID: "t1", ActorID: "machine:desk-a"}
	command := ProcessCommand{Content: "请补充发生时间", IdempotencyKey: "request-info-key"}
	if _, err := service.Process(context.Background(), operator, "feedback-1", "request-info", command); err != nil {
		t.Fatal(err)
	}
	if repo.value.Status != StatusNeedCustomerInfo {
		t.Fatalf("status=%s", repo.value.Status)
	}
	logCount, messageCount := len(repo.logs), len(repo.messages)
	if _, err := service.Process(context.Background(), operator, "feedback-1", "request-info", command); err != nil {
		t.Fatal(err)
	}
	if len(repo.logs) != logCount || len(repo.messages) != messageCount {
		t.Fatalf("replay changed state logs=%d messages=%d", len(repo.logs), len(repo.messages))
	}
}

func TestCustomerCloseIsDurablyIdempotentAndDoesNotDuplicateEffects(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	repo := &memoryRepo{value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1", Version: 1}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1", Status: StatusResolved}}
	service := NewService(repo, nil, contactStub{}, fixedClock{now}, &ids{})
	actor := CustomerActor{TenantID: "t1", CustomerID: 2, AccountID: "sub-1"}
	command := CloseCommand{IdempotencyKey: "close-key-1"}
	first, err := service.Close(context.Background(), actor, "feedback-1", command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Feedback.Status != StatusClosed || len(repo.logs) != 1 || len(repo.outbox) != 1 {
		t.Fatalf("first=%+v logs=%d outbox=%d", first.Feedback, len(repo.logs), len(repo.outbox))
	}
	stored := repo.logs[0]
	if stored.IdempotencyKey == nil || *stored.IdempotencyKey != command.IdempotencyKey || stored.ActorType != "CUSTOMER" || stored.ActorID != actor.AccountID || stored.RequestHash == "" {
		t.Fatalf("status action was not durably bound: %#v", stored)
	}
	second, err := service.Close(context.Background(), actor, "feedback-1", command)
	if err != nil || second.Feedback.Status != StatusClosed || len(repo.logs) != 1 || len(repo.outbox) != 1 {
		t.Fatalf("replay=%+v err=%v logs=%d outbox=%d", second, err, len(repo.logs), len(repo.outbox))
	}
}

func TestCustomerCloseRejectsTenantKeyUsedByAnotherResourceOrActor(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	key := "close-key-1"
	tests := []struct {
		name string
		log  StatusLog
	}{
		{name: "different feedback", log: StatusLog{TenantID: "t1", FeedbackID: 99, ActorType: "CUSTOMER", ActorID: "sub-1", IdempotencyKey: &key, RequestHash: "opaque"}},
		{name: "different actor", log: StatusLog{TenantID: "t1", FeedbackID: 1, ActorType: "CUSTOMER", ActorID: "sub-2", IdempotencyKey: &key, RequestHash: "opaque"}},
		{name: "different actor type", log: StatusLog{TenantID: "t1", FeedbackID: 1, ActorType: "OPERATOR", ActorID: "sub-1", IdempotencyKey: &key, RequestHash: "opaque"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &memoryRepo{
				value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1", Version: 1}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1", Status: StatusResolved},
				logs:  []StatusLog{test.log},
			}
			service := NewService(repo, nil, contactStub{}, fixedClock{now}, &ids{})
			_, err := service.Close(context.Background(), CustomerActor{TenantID: "t1", CustomerID: 2, AccountID: "sub-1"}, "feedback-1", CloseCommand{IdempotencyKey: key})
			if !errors.Is(err, ErrIdempotencyConflict) || repo.value.Status != StatusResolved || len(repo.logs) != 1 || len(repo.outbox) != 0 {
				t.Fatalf("err=%v status=%s logs=%d outbox=%d", err, repo.value.Status, len(repo.logs), len(repo.outbox))
			}
		})
	}
}

func TestCustomerCloseChecksVisibilityBeforeTenantKeyReplay(t *testing.T) {
	key := "close-key-1"
	repo := &memoryRepo{
		value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1", Version: 1}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1", Status: StatusClosed},
		logs:  []StatusLog{{TenantID: "t1", FeedbackID: 1, ActorType: "CUSTOMER", ActorID: "sub-1", IdempotencyKey: &key}},
	}
	service := NewService(repo, nil, contactStub{}, fixedClock{}, &ids{})
	_, err := service.Close(context.Background(), CustomerActor{TenantID: "t1", CustomerID: 2, AccountID: "sub-2"}, "feedback-1", CloseCommand{IdempotencyKey: key})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account learned replay state: %v", err)
	}
}

func TestOperatorStatusKeyConflictsAcrossResourceAndActor(t *testing.T) {
	key := "accept-key-1"
	repo := &memoryRepo{
		value: &Feedback{ActorModel: ActorModel{ID: 1, TenantID: "t1", Version: 1}, PublicID: "feedback-1", CustomerID: 2, AccountID: "sub-1", Status: StatusSubmitted},
		logs:  []StatusLog{{TenantID: "t1", FeedbackID: 99, ActorType: "OPERATOR", ActorID: "machine:desk-a", IdempotencyKey: &key, RequestHash: "opaque"}},
	}
	service := NewService(repo, nil, contactStub{}, fixedClock{}, &ids{})
	_, err := service.Process(context.Background(), OperatorActor{TenantID: "t1", ActorID: "machine:desk-a"}, "feedback-1", "accept", ProcessCommand{IdempotencyKey: key})
	if !errors.Is(err, ErrIdempotencyConflict) || repo.value.Status != StatusSubmitted || len(repo.outbox) != 0 {
		t.Fatalf("cross-resource key was accepted: err=%v status=%s", err, repo.value.Status)
	}
}
