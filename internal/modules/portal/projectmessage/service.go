package projectmessage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var (
	ErrNotFound             = apperror.New(http.StatusNotFound, "PORTAL_PROJECT_CONVERSATION_NOT_FOUND", "project conversation not found")
	ErrProjectNotFound      = apperror.New(http.StatusNotFound, "PORTAL_PROJECT_MESSAGE_PROJECT_NOT_FOUND", "project not found")
	ErrRecipientUnavailable = apperror.New(http.StatusConflict, "PORTAL_PROJECT_MANAGER_RECIPIENT_UNAVAILABLE", "project manager station-message recipient is unavailable")
	ErrValidation           = apperror.New(http.StatusUnprocessableEntity, "PORTAL_PROJECT_MESSAGE_VALIDATION_ERROR", "project message request is invalid")
	ErrIdempotencyConflict  = apperror.New(http.StatusConflict, "PORTAL_PROJECT_MESSAGE_IDEMPOTENCY_CONFLICT", "idempotency key was used with another request")
	ErrRateLimited          = apperror.New(http.StatusTooManyRequests, "PORTAL_PROJECT_MESSAGE_RATE_LIMITED", "too many project messages")
	ErrConflict             = apperror.New(http.StatusConflict, "PORTAL_PROJECT_MESSAGE_CONFLICT", "project conversation changed; retry request")
	ErrInvalidReadCursor    = apperror.New(http.StatusUnprocessableEntity, "PORTAL_PROJECT_MESSAGE_READ_CURSOR_INVALID", "project message read cursor is invalid")
	ErrInvalidPageCursor    = apperror.New(http.StatusBadRequest, "PORTAL_PROJECT_MESSAGE_PAGE_CURSOR_INVALID", "project message page cursor is invalid")
)

const (
	maxMessagesPerWindow int64 = 10
	rateWindow                 = 5 * time.Minute
)

type CustomerActor struct {
	TenantID, AccountID string
	CustomerID          uint64
}
type ManagerActor struct{ TenantID, AccountID string }
type ProjectRecipient struct{ ProjectID, ManagerName, ManagerPortalAccountID string }
type SendCommand struct{ Content, IdempotencyKey string }
type CreateCommand struct{ IdempotencyKey string }
type ReadCommand struct {
	// MessageCursors contains only recipient messages that the client actually
	// rendered. Per-message receipts preserve unread holes across bounded pages.
	MessageCursors []string
	// LegacyThroughMessageCursor is accepted as one exact message receipt for
	// the previous client. It no longer advances across intermediate messages.
	LegacyThroughMessageCursor string
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }

type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	LockCustomerAccount(context.Context, CustomerActor) error
	FindProjectRecipient(context.Context, CustomerActor, string) (ProjectRecipient, error)
	LockProjectRecipient(context.Context, string, uint64, string) (ProjectRecipient, error)
	FindCreateReplay(context.Context, CustomerActor, string) (*Conversation, error)
	FindByRecipient(context.Context, CustomerActor, string, string) (*Conversation, error)
	CreateConversation(context.Context, *Conversation) error
	ListCustomer(context.Context, CustomerActor, string, int, int) (pagination.Page[Conversation], error)
	FindCurrentCustomerConversation(context.Context, CustomerActor, string) (*Conversation, error)
	FindCustomer(context.Context, CustomerActor, string) (*Conversation, error)
	FindCustomerConversationByPublicID(context.Context, CustomerActor, string) (*Conversation, error)
	FindManager(context.Context, ManagerActor, string) (*Conversation, error)
	ListManager(context.Context, ManagerActor, int, int) (pagination.Page[Conversation], error)
	FindForUpdate(context.Context, string, uint64) (*Conversation, error)
	FindMessageReplay(context.Context, string, string, string, string) (*Message, error)
	CreateMessage(context.Context, *Message) error
	CountRecent(context.Context, string, uint64, string, string, time.Time) (int64, error)
	ListMessages(context.Context, string, uint64, string, int) (MessagePage, error)
	FindRecipientMessage(context.Context, string, uint64, string, string) (*Message, error)
	FindMessageReadReceipt(context.Context, string, uint64, string, string, uint64) (*MessageReadReceipt, error)
	CreateMessageReadReceipt(context.Context, *MessageReadReceipt) error
	CountUnread(context.Context, string, uint64, string, string) (int64, error)
	TouchConversation(context.Context, *Conversation, time.Time) error
	CreateEvent(context.Context, *Event) error
}

type Service struct {
	repo  Repository
	clock Clock
	ids   IDGenerator
}

func NewService(repo Repository, clock Clock, ids IDGenerator) *Service {
	return &Service{repo: repo, clock: clock, ids: ids}
}

func (s *Service) Create(ctx context.Context, actor CustomerActor, projectID string, command CreateCommand) (*Conversation, error) {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if !validCustomer(actor) || !validOpaque(projectID, 64) || !validKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	recipient, err := s.repo.FindProjectRecipient(ctx, actor, projectID)
	if err != nil {
		return nil, err
	}
	if !validOpaque(recipient.ManagerPortalAccountID, 128) {
		return nil, ErrRecipientUnavailable
	}
	hash := digest(projectID, recipient.ManagerPortalAccountID)
	if replay, findErr := s.repo.FindCreateReplay(ctx, actor, command.IdempotencyKey); findErr == nil {
		if replay.CreateRequestHash != hash {
			return nil, ErrIdempotencyConflict
		}
		return replay, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}
	if existing, findErr := s.repo.FindByRecipient(ctx, actor, projectID, recipient.ManagerPortalAccountID); findErr == nil {
		if existing.CreateIdempotencyKey != command.IdempotencyKey || existing.CreateRequestHash != hash {
			return nil, ErrConflict
		}
		return existing, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}
	now := s.clock.Now().UTC()
	publicID := s.ids.NewID()
	if !validOpaque(publicID, 64) {
		return nil, errors.New("secure project conversation identifier generation failed")
	}
	value := &Conversation{PublicID: publicID, TenantID: actor.TenantID, CustomerID: actor.CustomerID, ProjectID: projectID, CustomerAccountID: actor.AccountID, ManagerAccountIDSnapshot: recipient.ManagerPortalAccountID, ManagerNameSnapshot: strings.TrimSpace(recipient.ManagerName), CreateIdempotencyKey: command.IdempotencyKey, CreateRequestHash: hash, CreatedAt: now, UpdatedAt: now, Version: 1}
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		// Re-resolve the source-owned recipient while the write is executing. A
		// changed or missing mapping must never create a thread for a stale name.
		current, findErr := s.repo.LockProjectRecipient(tx, actor.TenantID, actor.CustomerID, projectID)
		if findErr != nil {
			return findErr
		}
		if current.ManagerPortalAccountID == "" || current.ManagerPortalAccountID != recipient.ManagerPortalAccountID {
			return ErrRecipientUnavailable
		}
		if createErr := s.repo.CreateConversation(tx, value); createErr != nil {
			return createErr
		}
		return s.repo.CreateEvent(tx, event(value, nil, "CONVERSATION_CREATED", SenderCustomer, actor.AccountID, recipient.ManagerPortalAccountID, ctx, now))
	})
	if err != nil {
		if replay, findErr := s.repo.FindCreateReplay(ctx, actor, command.IdempotencyKey); findErr == nil {
			if replay.CreateRequestHash != hash {
				return nil, ErrIdempotencyConflict
			}
			return replay, nil
		}
		if existing, findErr := s.repo.FindByRecipient(ctx, actor, projectID, recipient.ManagerPortalAccountID); findErr == nil {
			if existing.CreateIdempotencyKey == command.IdempotencyKey && existing.CreateRequestHash == hash {
				return existing, nil
			}
			return nil, ErrConflict
		}
	}
	return value, err
}

func (s *Service) ListCustomer(ctx context.Context, actor CustomerActor, projectID string, page, pageSize int) (pagination.Page[Conversation], error) {
	if !validCustomer(actor) || !validOpaque(projectID, 64) {
		return pagination.Page[Conversation]{}, ErrProjectNotFound
	}
	if _, err := s.repo.FindProjectRecipient(ctx, actor, projectID); err != nil {
		return pagination.Page[Conversation]{}, err
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	return s.repo.ListCustomer(ctx, actor, projectID, page, pageSize)
}
func (s *Service) CurrentCustomer(ctx context.Context, actor CustomerActor, projectID, before string, pageSize int) (*Detail, error) {
	if !validCustomer(actor) || !validOpaque(projectID, 64) {
		return nil, ErrProjectNotFound
	}
	value, err := s.repo.FindCurrentCustomerConversation(ctx, actor, projectID)
	if err != nil {
		return nil, err
	}
	pageSize = normalizeMessagePageSize(pageSize)
	return s.authorizedDetail(ctx, value, strings.TrimSpace(before), pageSize, SenderCustomer, actor.AccountID, s.authorizeCustomer(actor))
}
func (s *Service) GetCustomer(ctx context.Context, actor CustomerActor, publicID, before string, pageSize int) (*Detail, error) {
	if !validCustomer(actor) || !validOpaque(publicID, 64) {
		return nil, ErrNotFound
	}
	value, err := s.repo.FindCustomerConversationByPublicID(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	pageSize = normalizeMessagePageSize(pageSize)
	return s.authorizedDetail(ctx, value, strings.TrimSpace(before), pageSize, SenderCustomer, actor.AccountID, s.authorizeCustomer(actor))
}
func (s *Service) ListManager(ctx context.Context, actor ManagerActor, page, pageSize int) (pagination.Page[Conversation], error) {
	if !validManager(actor) {
		return pagination.Page[Conversation]{}, ErrValidation
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	return s.repo.ListManager(ctx, actor, page, pageSize)
}
func (s *Service) GetManager(ctx context.Context, actor ManagerActor, publicID, before string, pageSize int) (*Detail, error) {
	if !validManager(actor) || !validOpaque(publicID, 64) {
		return nil, ErrNotFound
	}
	value, err := s.repo.FindManager(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	pageSize = normalizeMessagePageSize(pageSize)
	return s.authorizedDetail(ctx, value, strings.TrimSpace(before), pageSize, SenderManager, actor.AccountID, s.authorizeManager(actor))
}
func (s *Service) SendCustomer(ctx context.Context, actor CustomerActor, publicID string, command SendCommand) (*Detail, error) {
	if !validCustomer(actor) {
		return nil, ErrValidation
	}
	value, err := s.repo.FindCustomerConversationByPublicID(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	return s.send(ctx, value, SenderCustomer, actor.AccountID, value.ManagerAccountIDSnapshot, command, s.authorizeCustomer(actor))
}
func (s *Service) SendManager(ctx context.Context, actor ManagerActor, publicID string, command SendCommand) (*Detail, error) {
	if !validManager(actor) {
		return nil, ErrValidation
	}
	value, err := s.repo.FindManager(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	return s.send(ctx, value, SenderManager, actor.AccountID, value.CustomerAccountID, command, s.authorizeManager(actor))
}

func (s *Service) ReadCustomer(ctx context.Context, actor CustomerActor, publicID string, command ReadCommand) (*ReadState, error) {
	if !validCustomer(actor) || !validOpaque(publicID, 64) {
		return nil, ErrNotFound
	}
	value, err := s.repo.FindCustomerConversationByPublicID(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	return s.markRead(ctx, value, SenderCustomer, actor.AccountID, command, s.authorizeCustomer(actor))
}

func (s *Service) ReadManager(ctx context.Context, actor ManagerActor, publicID string, command ReadCommand) (*ReadState, error) {
	if !validManager(actor) || !validOpaque(publicID, 64) {
		return nil, ErrNotFound
	}
	value, err := s.repo.FindManager(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	return s.markRead(ctx, value, SenderManager, actor.AccountID, command, s.authorizeManager(actor))
}

func (s *Service) markRead(ctx context.Context, value *Conversation, readerType, readerAccountID string, command ReadCommand, authorized func(context.Context, *Conversation) error) (*ReadState, error) {
	cursors := make([]string, 0, len(command.MessageCursors)+1)
	if legacy := strings.TrimSpace(command.LegacyThroughMessageCursor); legacy != "" {
		if len(command.MessageCursors) != 0 {
			return nil, ErrInvalidReadCursor
		}
		cursors = append(cursors, legacy)
	} else {
		cursors = append(cursors, command.MessageCursors...)
	}
	if len(cursors) == 0 || len(cursors) > 100 {
		return nil, ErrInvalidReadCursor
	}
	seen := make(map[string]struct{}, len(cursors))
	for i := range cursors {
		cursors[i] = strings.TrimSpace(cursors[i])
		if !validOpaque(cursors[i], 64) {
			return nil, ErrInvalidReadCursor
		}
		if _, duplicate := seen[cursors[i]]; duplicate {
			return nil, ErrInvalidReadCursor
		}
		seen[cursors[i]] = struct{}{}
	}
	now := s.clock.Now().UTC()
	var state *ReadState
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		locked, lockErr := s.repo.FindForUpdate(tx, value.TenantID, value.ID)
		if lockErr != nil {
			return lockErr
		}
		if authorizeErr := authorized(tx, locked); authorizeErr != nil {
			return authorizeErr
		}
		acknowledged := make([]string, 0, len(cursors))
		for _, cursor := range cursors {
			target, targetErr := s.repo.FindRecipientMessage(tx, locked.TenantID, locked.ID, cursor, readerAccountID)
			if targetErr != nil {
				return targetErr
			}
			if _, receiptErr := s.repo.FindMessageReadReceipt(tx, locked.TenantID, locked.ID, readerType, readerAccountID, target.ID); receiptErr == nil {
				acknowledged = append(acknowledged, cursor)
				continue
			} else if !errors.Is(receiptErr, ErrNotFound) {
				return receiptErr
			}
			receipt := &MessageReadReceipt{TenantID: locked.TenantID, ConversationID: locked.ID, MessageID: target.ID, ReaderType: readerType, ReaderAccountID: readerAccountID, ReadAt: now, CreatedAt: now}
			if createErr := s.repo.CreateMessageReadReceipt(tx, receipt); createErr != nil {
				return createErr
			}
			if eventErr := s.repo.CreateEvent(tx, event(locked, target, "MESSAGE_READ_RECORDED", readerType, readerAccountID, "", ctx, now)); eventErr != nil {
				return eventErr
			}
			acknowledged = append(acknowledged, cursor)
		}
		unread, countErr := s.repo.CountUnread(tx, locked.TenantID, locked.ID, readerType, readerAccountID)
		if countErr != nil {
			return countErr
		}
		state = &ReadState{AcknowledgedMessageCursors: acknowledged, ReadAt: now, UnreadCount: unread}
		return nil
	})
	return state, err
}
func (s *Service) send(ctx context.Context, value *Conversation, senderType, senderID, recipientID string, command SendCommand, authorized func(context.Context, *Conversation) error) (*Detail, error) {
	command.Content = normalizeText(command.Content)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if !validText(command.Content) || !validKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	hash := digest(value.PublicID, command.Content, recipientID)
	now := s.clock.Now().UTC()
	var detail *Detail
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		// The conversation row serializes the rate window. The actor-specific
		// authorization below also locks the current identity/recipient source.
		locked, lockErr := s.repo.FindForUpdate(tx, value.TenantID, value.ID)
		if lockErr != nil {
			return lockErr
		}
		if authorizeErr := authorized(tx, locked); authorizeErr != nil {
			return authorizeErr
		}
		if replay, replayErr := s.repo.FindMessageReplay(tx, value.TenantID, senderType, senderID, command.IdempotencyKey); replayErr == nil {
			if replay.ConversationID != value.ID || replay.RequestHash != hash {
				return ErrIdempotencyConflict
			}
			messages, listErr := s.repo.ListMessages(tx, value.TenantID, locked.ID, "", 100)
			if listErr == nil {
				state, stateErr := s.loadReadState(tx, locked, senderType, senderID)
				if stateErr != nil {
					return stateErr
				}
				detail = &Detail{Conversation: *locked, Messages: messages, ReadState: state}
			}
			return listErr
		} else if !errors.Is(replayErr, ErrNotFound) {
			return replayErr
		}
		count, countErr := s.repo.CountRecent(tx, value.TenantID, value.ID, senderType, senderID, now.Add(-rateWindow))
		if countErr != nil {
			return countErr
		}
		if count >= maxMessagesPerWindow {
			return ErrRateLimited
		}
		cursor := digest(s.ids.NewID(), value.PublicID, senderType, senderID, command.IdempotencyKey, now.Format(time.RFC3339Nano))
		message := &Message{Cursor: cursor, TenantID: value.TenantID, ConversationID: value.ID, SenderType: senderType, SenderAccountID: senderID, RecipientAccountID: recipientID, Content: command.Content, IdempotencyKey: command.IdempotencyKey, RequestHash: hash, AcceptedAt: now}
		if createErr := s.repo.CreateMessage(tx, message); createErr != nil {
			return createErr
		}
		if touchErr := s.repo.TouchConversation(tx, locked, now); touchErr != nil {
			return touchErr
		}
		if eventErr := s.repo.CreateEvent(tx, event(locked, message, "MESSAGE_ACCEPTED", senderType, senderID, recipientID, ctx, now)); eventErr != nil {
			return eventErr
		}
		messages, listErr := s.repo.ListMessages(tx, value.TenantID, locked.ID, "", 100)
		if listErr == nil {
			state, stateErr := s.loadReadState(tx, locked, senderType, senderID)
			if stateErr != nil {
				return stateErr
			}
			detail = &Detail{Conversation: *locked, Messages: messages, ReadState: state}
		}
		return listErr
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

type Detail struct {
	Conversation Conversation `json:"conversation"`
	Messages     MessagePage  `json:"messages"`
	ReadState    *ReadState   `json:"read_state,omitempty"`
}

type MessagePage struct {
	Items      []Message `json:"items"`
	PageSize   int       `json:"page_size"`
	Total      int64     `json:"total"`
	HasMore    bool      `json:"has_more"`
	NextBefore string    `json:"next_before,omitempty"`
}

type ReadState struct {
	AcknowledgedMessageCursors []string  `json:"acknowledged_message_cursors,omitempty"`
	ReadAt                     time.Time `json:"read_at,omitempty"`
	UnreadCount                int64     `json:"unread_count"`
}

func (s *Service) authorizedDetail(ctx context.Context, value *Conversation, before string, pageSize int, readerType, readerAccountID string, authorized func(context.Context, *Conversation) error) (*Detail, error) {
	if before != "" && !validOpaque(before, 64) {
		return nil, ErrInvalidPageCursor
	}
	var detail *Detail
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		locked, err := s.repo.FindForUpdate(tx, value.TenantID, value.ID)
		if err != nil {
			return err
		}
		if err = authorized(tx, locked); err != nil {
			return err
		}
		messages, err := s.repo.ListMessages(tx, locked.TenantID, locked.ID, before, pageSize)
		if err != nil {
			return err
		}
		state, err := s.loadReadState(tx, locked, readerType, readerAccountID)
		if err != nil {
			return err
		}
		detail = &Detail{Conversation: *locked, Messages: messages, ReadState: state}
		return nil
	})
	return detail, err
}

func (s *Service) loadReadState(ctx context.Context, value *Conversation, readerType, readerAccountID string) (*ReadState, error) {
	unread, err := s.repo.CountUnread(ctx, value.TenantID, value.ID, readerType, readerAccountID)
	if err != nil {
		return nil, err
	}
	return &ReadState{UnreadCount: unread}, nil
}

func normalizeMessagePageSize(pageSize int) int {
	if pageSize <= 0 {
		return pagination.DefaultPageSize
	}
	if pageSize > pagination.MaxPageSize {
		return pagination.MaxPageSize
	}
	return pageSize
}

func (s *Service) authorizeCustomer(actor CustomerActor) func(context.Context, *Conversation) error {
	return func(tx context.Context, locked *Conversation) error {
		if locked.CustomerID != actor.CustomerID || locked.CustomerAccountID != actor.AccountID {
			return ErrNotFound
		}
		if err := s.repo.LockCustomerAccount(tx, actor); err != nil {
			return err
		}
		current, err := s.repo.LockProjectRecipient(tx, actor.TenantID, actor.CustomerID, locked.ProjectID)
		if err != nil {
			return err
		}
		if current.ManagerPortalAccountID == "" || current.ManagerPortalAccountID != locked.ManagerAccountIDSnapshot {
			return ErrRecipientUnavailable
		}
		return nil
	}
}

func (s *Service) authorizeManager(actor ManagerActor) func(context.Context, *Conversation) error {
	return func(tx context.Context, locked *Conversation) error {
		if locked.ManagerAccountIDSnapshot != actor.AccountID {
			return ErrNotFound
		}
		current, err := s.repo.LockProjectRecipient(tx, actor.TenantID, locked.CustomerID, locked.ProjectID)
		if err != nil {
			return err
		}
		if current.ManagerPortalAccountID != actor.AccountID {
			return ErrNotFound
		}
		return nil
	}
}
func event(value *Conversation, message *Message, operation, actorType, actorID, recipientID string, ctx context.Context, at time.Time) *Event {
	result := &Event{TenantID: value.TenantID, ConversationID: value.ID, Operation: operation, ActorType: actorType, ActorAccountID: actorID, RecipientAccountID: recipientID, RequestID: requestctx.ID(ctx), Result: "SUCCEEDED", OccurredAt: at}
	if message != nil {
		result.MessageID = &message.ID
	}
	return result
}
func validCustomer(a CustomerActor) bool {
	return validOpaque(a.TenantID, 64) && a.CustomerID != 0 && validOpaque(a.AccountID, 128)
}
func validManager(a ManagerActor) bool {
	return validOpaque(a.TenantID, 64) && validOpaque(a.AccountID, 128)
}
func validOpaque(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max
}
func validKey(value string) bool {
	return validOpaque(value, 128) && utf8.RuneCountInString(value) >= 8
}
func validText(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 2000 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
func normalizeText(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}
func digest(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}
