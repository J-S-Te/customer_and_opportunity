package projectmessage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fixedIDs struct{ value string }

func (i fixedIDs) NewID() string { return i.value }

type memoryRepository struct {
	recipient               ProjectRecipient
	conversation            *Conversation
	messages                []Message
	receipts                []MessageReadReceipt
	events                  []Event
	locked                  bool
	lockedRecipientOverride string
	listMessageCalls        int
}

func (r *memoryRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *memoryRepository) LockCustomerAccount(context.Context, CustomerActor) error {
	r.locked = true
	return nil
}
func (r *memoryRepository) FindProjectRecipient(_ context.Context, actor CustomerActor, projectID string) (ProjectRecipient, error) {
	if actor.TenantID != "tenant-a" || actor.CustomerID != 7 || projectID != "P/1" {
		return ProjectRecipient{}, ErrProjectNotFound
	}
	return r.recipient, nil
}
func (r *memoryRepository) LockProjectRecipient(_ context.Context, tenant string, customer uint64, projectID string) (ProjectRecipient, error) {
	if tenant != "tenant-a" || customer != 7 || projectID != "P/1" {
		return ProjectRecipient{}, ErrProjectNotFound
	}
	value := r.recipient
	if r.lockedRecipientOverride != "" {
		value.ManagerPortalAccountID = r.lockedRecipientOverride
	}
	return value, nil
}
func (r *memoryRepository) FindCreateReplay(_ context.Context, actor CustomerActor, key string) (*Conversation, error) {
	if r.conversation != nil && r.conversation.CustomerAccountID == actor.AccountID && r.conversation.CreateIdempotencyKey == key {
		return r.conversation, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) FindByRecipient(_ context.Context, actor CustomerActor, projectID, manager string) (*Conversation, error) {
	if r.conversation != nil && r.conversation.CustomerAccountID == actor.AccountID && r.conversation.ProjectID == projectID && r.conversation.ManagerAccountIDSnapshot == manager {
		return r.conversation, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) CreateConversation(_ context.Context, value *Conversation) error {
	value.ID = 1
	r.conversation = value
	return nil
}
func (r *memoryRepository) ListCustomer(context.Context, CustomerActor, string, int, int) (pagination.Page[Conversation], error) {
	panic("not used")
}
func (r *memoryRepository) FindCurrentCustomerConversation(_ context.Context, actor CustomerActor, projectID string) (*Conversation, error) {
	return r.FindByRecipient(context.Background(), actor, projectID, r.recipient.ManagerPortalAccountID)
}
func (r *memoryRepository) FindCustomer(_ context.Context, actor CustomerActor, publicID string) (*Conversation, error) {
	if r.conversation != nil && r.conversation.CustomerAccountID == actor.AccountID && r.conversation.PublicID == publicID {
		return r.conversation, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) FindCustomerConversationByPublicID(ctx context.Context, actor CustomerActor, publicID string) (*Conversation, error) {
	value, err := r.FindCustomer(ctx, actor, publicID)
	if err == nil && value.ManagerAccountIDSnapshot != r.recipient.ManagerPortalAccountID {
		return nil, ErrNotFound
	}
	return value, err
}
func (r *memoryRepository) FindManager(_ context.Context, actor ManagerActor, publicID string) (*Conversation, error) {
	if r.conversation != nil && r.conversation.ManagerAccountIDSnapshot == actor.AccountID && r.conversation.PublicID == publicID && r.recipient.ManagerPortalAccountID == actor.AccountID {
		return r.conversation, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) ListManager(context.Context, ManagerActor, int, int) (pagination.Page[Conversation], error) {
	panic("not used")
}
func (r *memoryRepository) FindForUpdate(_ context.Context, tenant string, id uint64) (*Conversation, error) {
	if r.conversation != nil && r.conversation.TenantID == tenant && r.conversation.ID == id {
		return r.conversation, nil
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) FindMessageReplay(_ context.Context, tenant, senderType, sender, key string) (*Message, error) {
	for i := range r.messages {
		v := &r.messages[i]
		if v.TenantID == tenant && v.SenderType == senderType && v.SenderAccountID == sender && v.IdempotencyKey == key {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) CreateMessage(_ context.Context, value *Message) error {
	value.ID = uint64(len(r.messages) + 1)
	r.messages = append(r.messages, *value)
	return nil
}
func (r *memoryRepository) CountRecent(_ context.Context, tenant string, conversationID uint64, senderType, sender string, since time.Time) (int64, error) {
	var n int64
	for i := range r.messages {
		v := r.messages[i]
		if v.TenantID == tenant && v.ConversationID == conversationID && v.SenderType == senderType && v.SenderAccountID == sender && !v.AcceptedAt.Before(since) {
			n++
		}
	}
	return n, nil
}
func (r *memoryRepository) ListMessages(_ context.Context, tenant string, conversationID uint64, before string, pageSize int) (MessagePage, error) {
	r.listMessageCalls++
	items := []Message{}
	anchor := len(r.messages)
	if before != "" {
		anchor = -1
		for i := range r.messages {
			if r.messages[i].Cursor == before {
				anchor = i
				break
			}
		}
		if anchor < 0 {
			return MessagePage{}, ErrInvalidPageCursor
		}
	}
	for i := range r.messages {
		if i < anchor && r.messages[i].TenantID == tenant && r.messages[i].ConversationID == conversationID {
			items = append(items, r.messages[i])
		}
	}
	if len(items) > pageSize {
		items = items[len(items)-pageSize:]
	}
	page := MessagePage{Items: items, PageSize: pageSize, Total: int64(len(r.messages))}
	if len(items) > 0 && len(items) < anchor {
		page.HasMore = true
		page.NextBefore = items[0].Cursor
	}
	return page, nil
}
func (r *memoryRepository) FindRecipientMessage(_ context.Context, tenant string, conversationID uint64, messageCursor, recipient string) (*Message, error) {
	for i := range r.messages {
		v := &r.messages[i]
		if v.TenantID == tenant && v.ConversationID == conversationID && v.Cursor == messageCursor && v.RecipientAccountID == recipient {
			return v, nil
		}
	}
	return nil, ErrInvalidReadCursor
}
func (r *memoryRepository) FindMessageReadReceipt(_ context.Context, tenant string, conversationID uint64, readerType, reader string, messageID uint64) (*MessageReadReceipt, error) {
	for i := range r.receipts {
		v := &r.receipts[i]
		if v.TenantID == tenant && v.ConversationID == conversationID && v.ReaderType == readerType && v.ReaderAccountID == reader && v.MessageID == messageID {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) CreateMessageReadReceipt(_ context.Context, value *MessageReadReceipt) error {
	value.ID = uint64(len(r.receipts) + 1)
	r.receipts = append(r.receipts, *value)
	return nil
}
func (r *memoryRepository) CountUnread(_ context.Context, tenant string, conversationID uint64, readerType, recipient string) (int64, error) {
	var count int64
	for i := range r.messages {
		v := r.messages[i]
		if v.TenantID == tenant && v.ConversationID == conversationID && v.RecipientAccountID == recipient {
			read := false
			for j := range r.receipts {
				if r.receipts[j].TenantID == tenant && r.receipts[j].ConversationID == conversationID && r.receipts[j].ReaderType == readerType && r.receipts[j].ReaderAccountID == recipient && r.receipts[j].MessageID == v.ID {
					read = true
				}
			}
			if read {
				continue
			}
			count++
		}
	}
	return count, nil
}
func (r *memoryRepository) TouchConversation(_ context.Context, value *Conversation, at time.Time) error {
	value.LastMessageAt = &at
	value.Version++
	return nil
}
func (r *memoryRepository) CreateEvent(_ context.Context, value *Event) error {
	r.events = append(r.events, *value)
	return nil
}

func messageFixture() (*Service, *memoryRepository, CustomerActor) {
	repo := &memoryRepository{recipient: ProjectRecipient{ProjectID: "P/1", ManagerName: "王经理", ManagerPortalAccountID: "manager-sub"}}
	service := NewService(repo, fixedClock{time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)}, fixedIDs{"conversation-public"})
	return service, repo, CustomerActor{TenantID: "tenant-a", CustomerID: 7, AccountID: "customer-sub"}
}

func TestCreateFailsClosedWithoutAuthoritativeManagerAccount(t *testing.T) {
	service, repo, actor := messageFixture()
	repo.recipient.ManagerPortalAccountID = ""
	if _, err := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"}); !errors.Is(err, ErrRecipientUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if repo.conversation != nil {
		t.Fatal("conversation created without recipient")
	}
}

func TestCreateBindsScopeRecipientAndIdempotency(t *testing.T) {
	service, repo, actor := messageFixture()
	value, err := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	if value.CustomerID != 7 || value.CustomerAccountID != "customer-sub" || value.ManagerAccountIDSnapshot != "manager-sub" || len(repo.events) != 1 {
		t.Fatalf("value=%#v events=%d", value, len(repo.events))
	}
	replay, err := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	if err != nil || replay != value {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if _, err = service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "another-key"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("new create key err=%v", err)
	}
}

func TestCustomerSendRevalidatesCurrentRecipientAndReplay(t *testing.T) {
	service, repo, actor := messageFixture()
	conversation, err := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.SendCustomer(context.Background(), actor, conversation.PublicID, SendCommand{Content: " 请确认\r\n本周计划 ", IdempotencyKey: "message-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !repo.locked || len(repo.messages) != 1 || repo.messages[0].Content != "请确认\n本周计划" || detail.Messages.Total != 1 {
		t.Fatalf("locked=%v messages=%#v detail=%#v", repo.locked, repo.messages, detail)
	}
	if _, err = service.SendCustomer(context.Background(), actor, conversation.PublicID, SendCommand{Content: "请确认\n本周计划", IdempotencyKey: "message-key-1"}); err != nil || len(repo.messages) != 1 {
		t.Fatalf("replay err=%v count=%d", err, len(repo.messages))
	}
	repo.lockedRecipientOverride = "manager-new"
	if _, err = service.SendCustomer(context.Background(), actor, conversation.PublicID, SendCommand{Content: "旧会话不应投递", IdempotencyKey: "message-key-2"}); !errors.Is(err, ErrRecipientUnavailable) {
		t.Fatalf("manager change err=%v", err)
	}
}

func TestMessageIdempotencyAndRateLimitAreConversationBound(t *testing.T) {
	service, _, actor := messageFixture()
	conversation, _ := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	for i := 0; i < 10; i++ {
		key := string(rune('a'+i)) + "-message-key"
		if _, err := service.SendCustomer(context.Background(), actor, conversation.PublicID, SendCommand{Content: "消息", IdempotencyKey: key}); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if _, err := service.SendCustomer(context.Background(), actor, conversation.PublicID, SendCommand{Content: "第十一条", IdempotencyKey: "message-key-11"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.SendCustomer(context.Background(), actor, conversation.PublicID, SendCommand{Content: "不同载荷", IdempotencyKey: "a-message-key"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestManagerCannotAccessAfterAuthoritativeAssignmentChanges(t *testing.T) {
	service, repo, actor := messageFixture()
	conversation, _ := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	repo.recipient.ManagerPortalAccountID = "manager-new"
	if _, err := service.GetManager(context.Background(), ManagerActor{TenantID: "tenant-a", AccountID: "manager-sub"}, conversation.PublicID, "", 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestDetailReadsRecheckLockedRecipientBeforeReturningMessages(t *testing.T) {
	service, repo, actor := messageFixture()
	conversation, _ := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	repo.messages = append(repo.messages, Message{TenantID: "tenant-a", ConversationID: conversation.ID, Content: "secret"})
	repo.lockedRecipientOverride = "manager-new"

	if _, err := service.GetManager(context.Background(), ManagerActor{TenantID: "tenant-a", AccountID: "manager-sub"}, conversation.PublicID, "", 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("manager detail err=%v", err)
	}
	if repo.listMessageCalls != 0 {
		t.Fatalf("old manager read %d message pages after reassignment", repo.listMessageCalls)
	}
	if _, err := service.GetCustomer(context.Background(), actor, conversation.PublicID, "", 100); !errors.Is(err, ErrRecipientUnavailable) {
		t.Fatalf("customer detail err=%v", err)
	}
	if repo.listMessageCalls != 0 {
		t.Fatalf("customer read %d stale message pages after reassignment", repo.listMessageCalls)
	}
}

func TestMessageReplayRechecksLockedRecipientBeforeReturningLatestPage(t *testing.T) {
	service, repo, actor := messageFixture()
	conversation, _ := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	command := SendCommand{Content: "reply", IdempotencyKey: "manager-key-1"}
	if _, err := service.SendManager(context.Background(), ManagerActor{TenantID: "tenant-a", AccountID: "manager-sub"}, conversation.PublicID, command); err != nil {
		t.Fatal(err)
	}
	repo.listMessageCalls = 0
	repo.lockedRecipientOverride = "manager-new"
	if _, err := service.SendManager(context.Background(), ManagerActor{TenantID: "tenant-a", AccountID: "manager-sub"}, conversation.PublicID, command); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay err=%v", err)
	}
	if repo.listMessageCalls != 0 {
		t.Fatalf("replay returned %d message pages after reassignment", repo.listMessageCalls)
	}
}

func TestManagerSendRechecksLockedAuthoritativeAssignment(t *testing.T) {
	service, repo, actor := messageFixture()
	conversation, _ := service.Create(context.Background(), actor, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	// Simulate reassignment after the manager's initial conversation lookup and
	// before the message transaction locks the authoritative snapshot.
	repo.recipient.ManagerPortalAccountID = "manager-new"
	if _, err := service.SendManager(context.Background(), ManagerActor{TenantID: "tenant-a", AccountID: "manager-sub"}, conversation.PublicID, SendCommand{Content: "旧经理回复", IdempotencyKey: "manager-key-1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	if len(repo.messages) != 0 {
		t.Fatal("old manager message was persisted after reassignment")
	}
}

func TestCustomerReadReceiptOnlyAdvancesAcrossAddressedMessages(t *testing.T) {
	service, repo, customer := messageFixture()
	conversation, err := service.Create(context.Background(), customer, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	manager := ManagerActor{TenantID: "tenant-a", AccountID: "manager-sub"}
	if _, err = service.SendManager(context.Background(), manager, conversation.PublicID, SendCommand{Content: "第一条", IdempotencyKey: "manager-key-1"}); err != nil {
		t.Fatal(err)
	}
	firstCursor := repo.messages[0].Cursor
	state, err := service.ReadCustomer(context.Background(), customer, conversation.PublicID, ReadCommand{MessageCursors: []string{firstCursor}})
	if err != nil || state.UnreadCount != 0 || len(state.AcknowledgedMessageCursors) != 1 || state.AcknowledgedMessageCursors[0] != firstCursor || len(repo.receipts) != 1 {
		t.Fatalf("state=%#v receipts=%#v err=%v", state, repo.receipts, err)
	}
	if _, err = service.ReadCustomer(context.Background(), customer, conversation.PublicID, ReadCommand{MessageCursors: []string{firstCursor}}); err != nil || len(repo.receipts) != 1 {
		t.Fatalf("idempotent read err=%v receipts=%d", err, len(repo.receipts))
	}
	if _, err = service.SendCustomer(context.Background(), customer, conversation.PublicID, SendCommand{Content: "自己发送", IdempotencyKey: "customer-key-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ReadCustomer(context.Background(), customer, conversation.PublicID, ReadCommand{MessageCursors: []string{repo.messages[1].Cursor}}); !errors.Is(err, ErrInvalidReadCursor) {
		t.Fatalf("customer acknowledged own message: %v", err)
	}
}

func TestManagerReadReceiptFailsAfterManagerChanges(t *testing.T) {
	service, repo, customer := messageFixture()
	conversation, _ := service.Create(context.Background(), customer, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	if _, err := service.SendCustomer(context.Background(), customer, conversation.PublicID, SendCommand{Content: "请查收", IdempotencyKey: "customer-key-1"}); err != nil {
		t.Fatal(err)
	}
	repo.lockedRecipientOverride = "manager-new"
	_, err := service.ReadManager(context.Background(), ManagerActor{TenantID: "tenant-a", AccountID: "manager-sub"}, conversation.PublicID, ReadCommand{MessageCursors: []string{repo.messages[0].Cursor}})
	if !errors.Is(err, ErrNotFound) || len(repo.receipts) != 0 {
		t.Fatalf("err=%v receipts=%#v", err, repo.receipts)
	}
}

func TestReadReceiptsDoNotCrossUndisplayedMessagesOverOneHundred(t *testing.T) {
	service, repo, customer := messageFixture()
	conversation, _ := service.Create(context.Background(), customer, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	for i := 0; i < 105; i++ {
		repo.messages = append(repo.messages, Message{ID: uint64(i + 1), Cursor: digest("cursor", string(rune(i+1))), TenantID: customer.TenantID, ConversationID: conversation.ID, SenderType: SenderManager, SenderAccountID: "manager-sub", RecipientAccountID: customer.AccountID})
	}
	visible := make([]string, 0, 100)
	for i := 5; i < 105; i++ {
		visible = append(visible, repo.messages[i].Cursor)
	}
	state, err := service.ReadCustomer(context.Background(), customer, conversation.PublicID, ReadCommand{MessageCursors: visible})
	if err != nil {
		t.Fatal(err)
	}
	if state.UnreadCount != 5 || len(repo.receipts) != 100 {
		t.Fatalf("state=%#v receipts=%d", state, len(repo.receipts))
	}
	if _, err = service.ReadCustomer(context.Background(), customer, conversation.PublicID, ReadCommand{MessageCursors: []string{visible[0], visible[0]}}); !errors.Is(err, ErrInvalidReadCursor) {
		t.Fatalf("duplicate cursors err=%v", err)
	}
	if len(repo.receipts) != 100 {
		t.Fatalf("duplicate request changed receipts=%d", len(repo.receipts))
	}
}

func TestMessageKeysetPageDoesNotDriftWhenNewMessageArrives(t *testing.T) {
	service, repo, customer := messageFixture()
	conversation, _ := service.Create(context.Background(), customer, "P/1", CreateCommand{IdempotencyKey: "create-key-1"})
	for i := 0; i < 6; i++ {
		repo.messages = append(repo.messages, Message{ID: uint64(i + 1), Cursor: digest("page-cursor", string(rune(i+1))), TenantID: customer.TenantID, ConversationID: conversation.ID})
	}
	first, err := service.GetCustomer(context.Background(), customer, conversation.PublicID, "", 3)
	if err != nil || !first.Messages.HasMore || len(first.Messages.Items) != 3 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	repo.messages = append(repo.messages, Message{ID: 7, Cursor: digest("concurrent"), TenantID: customer.TenantID, ConversationID: conversation.ID})
	older, err := service.GetCustomer(context.Background(), customer, conversation.PublicID, first.Messages.NextBefore, 3)
	if err != nil || len(older.Messages.Items) != 3 {
		t.Fatalf("older=%#v err=%v", older, err)
	}
	for i := range older.Messages.Items {
		for j := range first.Messages.Items {
			if older.Messages.Items[i].Cursor == first.Messages.Items[j].Cursor {
				t.Fatalf("keyset page duplicated cursor=%s", older.Messages.Items[i].Cursor)
			}
		}
	}
	if _, err = service.GetCustomer(context.Background(), customer, conversation.PublicID, "unknown-cursor", 3); !errors.Is(err, ErrInvalidPageCursor) {
		t.Fatalf("unknown anchor err=%v", err)
	}
}
