package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var (
	ErrNotFound            = apperror.New(http.StatusNotFound, "PORTAL_FEEDBACK_NOT_FOUND", "feedback not found")
	ErrValidation          = apperror.New(http.StatusUnprocessableEntity, "PORTAL_FEEDBACK_VALIDATION_ERROR", "feedback request is invalid")
	ErrInvalidTransition   = apperror.New(http.StatusConflict, "PORTAL_FEEDBACK_INVALID_TRANSITION", "feedback transition is not allowed")
	ErrIdempotencyConflict = apperror.New(http.StatusConflict, "PORTAL_FEEDBACK_IDEMPOTENCY_CONFLICT", "idempotency key was used with another request")
	ErrProjectForbidden    = apperror.New(http.StatusForbidden, "PORTAL_FEEDBACK_PROJECT_FORBIDDEN", "project is not accessible")
)

const maxFeedbackQueryPage = 1_000_000

const firstResponseSLA = 24 * time.Hour

type CustomerActor struct {
	TenantID, AccountID string
	CustomerID          uint64
}

// OperatorActor 只代表租户内运营身份，不携带客户范围；机器鉴权层必须先授予管理权限。
type OperatorActor struct{ TenantID, ActorID string }
type CreateCommand struct{ Type, Title, Description, ProjectID, ExpectedContact, IdempotencyKey string }
type MessageCommand struct{ Content, IdempotencyKey string }
type CloseCommand struct{ IdempotencyKey string }
type ProcessCommand struct{ Content, Reason, IdempotencyKey string }
type ListQuery struct {
	Status, Type   string
	Page, PageSize int
}
type ProjectAccess interface {
	Accessible(context.Context, string, uint64, string) (bool, error)
}
type ContactProtector interface {
	Encrypt(context.Context, string) ([]byte, string, error)
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }
type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	Create(context.Context, *Feedback) error
	FindByCreateKey(context.Context, CustomerActor, string) (*Feedback, error)
	ListCustomer(context.Context, CustomerActor, ListQuery) (pagination.Page[Feedback], error)
	FindCustomer(context.Context, CustomerActor, string) (*Feedback, error)
	FindOperator(context.Context, string, string) (*Feedback, error)
	ListOperator(context.Context, string, ListQuery) (pagination.Page[Feedback], error)
	FindForUpdate(context.Context, string, uint64) (*Feedback, error)
	Update(context.Context, *Feedback, uint64, map[string]any) error
	CreateMessage(context.Context, *Message) error
	FindMessageByKey(context.Context, string, uint64, string, string, string) (*Message, error)
	ListCustomerMessages(context.Context, string, uint64) ([]Message, error)
	ListStatusLogs(context.Context, string, uint64) ([]StatusLog, error)
	FindStatusActionByKey(context.Context, string, string) (*StatusLog, error)
	CreateStatusLog(context.Context, *StatusLog) error
	CreateOutbox(context.Context, *Outbox) error
}

type customerNotificationRepository interface {
	ListCustomerNotifications(context.Context, CustomerActor, bool, int, int) (pagination.Page[Notification], error)
	CountUnreadCustomerNotifications(context.Context, CustomerActor) (int64, error)
	FindCustomerNotificationForUpdate(context.Context, CustomerActor, uint64) (*Notification, error)
	MarkCustomerNotificationRead(context.Context, *Notification, time.Time) error
	CreateNotification(context.Context, *Notification) error
}

type CustomerNotificationView struct {
	ID         uint64    `json:"id"`
	FeedbackID string    `json:"feedback_id"`
	FeedbackNo string    `json:"feedback_no"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	TargetPath string    `json:"target_path"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Service) ListCustomerNotifications(ctx context.Context, actor CustomerActor, unreadOnly bool, page, pageSize int) (pagination.Page[CustomerNotificationView], error) {
	if !validCustomer(actor) {
		return pagination.Page[CustomerNotificationView]{}, ErrNotFound
	}
	repo, ok := s.repo.(customerNotificationRepository)
	if !ok {
		return pagination.Page[CustomerNotificationView]{}, ErrNotFound
	}
	items, err := repo.ListCustomerNotifications(ctx, actor, unreadOnly, page, pageSize)
	result := pagination.Page[CustomerNotificationView]{Items: make([]CustomerNotificationView, 0, len(items.Items)), Page: items.Page, PageSize: items.PageSize, Total: items.Total}
	for _, item := range items.Items {
		result.Items = append(result.Items, CustomerNotificationView{ID: item.ID, FeedbackID: item.PublicID, FeedbackNo: item.FeedbackNo, Kind: item.Kind, Title: item.Title, Body: item.Body, TargetPath: item.TargetPath, Status: item.Status, CreatedAt: item.CreatedAt})
	}
	return result, err
}

func (s *Service) UnreadCustomerNotificationCount(ctx context.Context, actor CustomerActor) (int64, error) {
	if !validCustomer(actor) {
		return 0, ErrNotFound
	}
	repo, ok := s.repo.(customerNotificationRepository)
	if !ok {
		return 0, ErrNotFound
	}
	return repo.CountUnreadCustomerNotifications(ctx, actor)
}

func (s *Service) ReadCustomerNotification(ctx context.Context, actor CustomerActor, id uint64) error {
	if !validCustomer(actor) || id == 0 {
		return ErrNotFound
	}
	repo, ok := s.repo.(customerNotificationRepository)
	if !ok {
		return ErrNotFound
	}
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, err := repo.FindCustomerNotificationForUpdate(tx, actor, id)
		if err != nil {
			return err
		}
		if value.Status == "READ" {
			return nil
		}
		if err := repo.MarkCustomerNotificationRead(tx, value, s.clock.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
}

type Service struct {
	repo     Repository
	projects ProjectAccess
	contacts ContactProtector
	clock    Clock
	ids      IDGenerator
}

func NewService(repo Repository, projects ProjectAccess, contacts ContactProtector, clock Clock, ids IDGenerator) *Service {
	return &Service{repo: repo, projects: projects, contacts: contacts, clock: clock, ids: ids}
}

func (s *Service) Create(ctx context.Context, actor CustomerActor, command CreateCommand) (*CustomerFeedback, error) {
	// 关联项目时先从可信投影验证客户归属；ExpectedContact 加密存储且只返回脱敏形式。
	normalizeCreate(&command)
	if !validCustomer(actor) || !validCreate(command) {
		return nil, ErrValidation
	}
	hash := payloadHash(command.Type, command.Title, command.Description, command.ProjectID, command.ExpectedContact)
	if existing, err := s.repo.FindByCreateKey(ctx, actor, command.IdempotencyKey); err == nil {
		if existing.CreateRequestHash != hash {
			return nil, ErrIdempotencyConflict
		}
		view := customerFeedback(existing)
		return &view, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if command.ProjectID != "" {
		allowed, err := s.projects.Accessible(ctx, actor.TenantID, actor.CustomerID, command.ProjectID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, ErrProjectForbidden
		}
	}
	contactCipher, contactMasked, err := s.contacts.Encrypt(ctx, command.ExpectedContact)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	publicID, feedbackNo, eventID := s.ids.NewID(), "FB-"+s.ids.NewID(), s.ids.NewID()
	if invalidGeneratedID(publicID) || invalidGeneratedID(eventID) || len(feedbackNo) > 48 {
		return nil, errors.New("secure feedback identifier generation failed")
	}
	value := &Feedback{ActorModel: ActorModel{TenantID: actor.TenantID, CreatedBy: actor.AccountID, UpdatedBy: actor.AccountID, CreatedAt: now, UpdatedAt: now, Version: 1}, PublicID: publicID, FeedbackNo: feedbackNo, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ProjectID: command.ProjectID, Type: command.Type, Title: command.Title, Description: command.Description, ExpectedContactCipher: contactCipher, ExpectedContactMasked: contactMasked, Status: StatusSubmitted, SubmittedAt: now, FirstResponseDueAt: now.Add(firstResponseSLA), CreateIdempotencyKey: command.IdempotencyKey, CreateRequestHash: hash}
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.repo.Create(tx, value); err != nil {
			return err
		}
		if err := s.repo.CreateStatusLog(tx, newStatusLog(ctx, value, "CUSTOMER", actor.AccountID, "", StatusSubmitted, "submitted", now)); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"feedback_id": value.PublicID, "feedback_no": value.FeedbackNo, "type": value.Type, "project_id": value.ProjectID})
		return s.repo.CreateOutbox(tx, &Outbox{EventID: eventID, TenantID: actor.TenantID, EventType: "PORTAL_FEEDBACK_SUBMITTED", AggregateID: value.ID, Payload: payload, Status: "PENDING", CreatedAt: now})
	})
	if err != nil {
		// 等待事务期间可能有并发请求提交同一幂等键；从持久化记录解析结果，不泄露数据库重复键错误。
		if existing, findErr := s.repo.FindByCreateKey(ctx, actor, command.IdempotencyKey); findErr == nil {
			if existing.CreateRequestHash != hash {
				return nil, ErrIdempotencyConflict
			}
			view := customerFeedback(existing)
			return &view, nil
		}
	}
	view := customerFeedback(value)
	return &view, err
}

func (s *Service) List(ctx context.Context, actor CustomerActor, query ListQuery) (pagination.Page[CustomerFeedback], error) {
	if !validCustomer(actor) {
		return pagination.Page[CustomerFeedback]{}, ErrNotFound
	}
	query.Status, query.Type = strings.TrimSpace(query.Status), strings.TrimSpace(query.Type)
	if query.Status != "" && !validStatus(Status(query.Status)) || query.Type != "" && !validType(query.Type) {
		return pagination.Page[CustomerFeedback]{}, ErrValidation
	}
	if query.Page < 0 || query.Page > maxFeedbackQueryPage || query.PageSize < 0 || query.PageSize > pagination.MaxPageSize {
		return pagination.Page[CustomerFeedback]{}, ErrValidation
	}
	query.Page, query.PageSize = pagination.Normalize(query.Page, query.PageSize)
	page, err := s.repo.ListCustomer(ctx, actor, query)
	result := pagination.Page[CustomerFeedback]{Items: make([]CustomerFeedback, 0, len(page.Items)), Page: page.Page, PageSize: page.PageSize, Total: page.Total}
	for i := range page.Items {
		result.Items = append(result.Items, customerFeedback(&page.Items[i]))
	}
	return result, err
}

func (s *Service) Get(ctx context.Context, actor CustomerActor, publicID string) (*CustomerTimeline, error) {
	if !validCustomer(actor) || strings.TrimSpace(publicID) == "" {
		return nil, ErrNotFound
	}
	value, err := s.repo.FindCustomer(ctx, actor, strings.TrimSpace(publicID))
	if err != nil {
		return nil, err
	}
	return s.timeline(ctx, value)
}

func (s *Service) AddCustomerMessage(ctx context.Context, actor CustomerActor, publicID string, command MessageCommand) (*CustomerTimeline, error) {
	// 客户补充材料可把 NEED_CUSTOMER_INFO 原子转回处理中，消息、状态日志和事件必须一起提交。
	command.Content, command.IdempotencyKey = strings.TrimSpace(command.Content), strings.TrimSpace(command.IdempotencyKey)
	if !validCustomer(actor) || !validText(command.Content, 1, 5000) || !validKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	value, err := s.repo.FindCustomer(ctx, actor, strings.TrimSpace(publicID))
	if err != nil {
		return nil, err
	}
	if value.Status == StatusClosed || value.Status == StatusRejected {
		return nil, ErrInvalidTransition
	}
	hash := payloadHash(command.Content)
	if existing, findErr := s.repo.FindMessageByKey(ctx, actor.TenantID, value.ID, "CUSTOMER", actor.AccountID, command.IdempotencyKey); findErr == nil {
		if existing.RequestHash != hash {
			return nil, ErrIdempotencyConflict
		}
		return s.timeline(ctx, value)
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}
	now := s.clock.Now().UTC()
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		locked, lockErr := s.repo.FindForUpdate(tx, actor.TenantID, value.ID)
		if lockErr != nil {
			return lockErr
		}
		if locked.CustomerID != actor.CustomerID || locked.AccountID != actor.AccountID {
			return ErrNotFound
		}
		if locked.Status == StatusClosed || locked.Status == StatusRejected {
			return ErrInvalidTransition
		}
		if err := s.repo.CreateMessage(tx, &Message{TenantID: actor.TenantID, FeedbackID: locked.ID, SenderType: "CUSTOMER", SenderID: actor.AccountID, Content: command.Content, Visibility: "CUSTOMER", IdempotencyKey: command.IdempotencyKey, RequestHash: hash, CreatedAt: now}); err != nil {
			return err
		}
		if locked.Status == StatusNeedCustomerInfo {
			if err := s.transition(tx, ctx, locked, StatusProcessing, "customer supplied requested information", "CUSTOMER", actor.AccountID, now, map[string]any{}); err != nil {
				return err
			}
		}
		return s.emit(tx, locked, "PORTAL_FEEDBACK_CUSTOMER_MESSAGE", now)
	})
	if err != nil {
		if existing, findErr := s.repo.FindMessageByKey(ctx, actor.TenantID, value.ID, "CUSTOMER", actor.AccountID, command.IdempotencyKey); findErr == nil {
			if existing.RequestHash != hash {
				return nil, ErrIdempotencyConflict
			}
			return s.Get(ctx, actor, publicID)
		}
		return nil, err
	}
	return s.Get(ctx, actor, publicID)
}

func (s *Service) Close(ctx context.Context, actor CustomerActor, publicID string, command CloseCommand) (*CustomerTimeline, error) {
	publicID, command.IdempotencyKey = strings.TrimSpace(publicID), strings.TrimSpace(command.IdempotencyKey)
	if !validCustomer(actor) || publicID == "" || !validKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	// 先确认资源可见性再查重放记录，避免幂等键成为探测其他 Portal 账号反馈单的侧信道。
	value, err := s.repo.FindCustomer(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	actionHash := statusActionHash(value, "CUSTOMER", actor.AccountID, "close", "", "")
	if replay, replayErr := s.statusActionReplay(ctx, value, "CUSTOMER", actor.AccountID, command.IdempotencyKey, actionHash); replayErr != nil {
		return nil, replayErr
	} else if replay {
		return s.timeline(ctx, value)
	}
	now := s.clock.Now().UTC()
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		locked, lockErr := s.repo.FindForUpdate(tx, actor.TenantID, value.ID)
		if lockErr != nil {
			return lockErr
		}
		if locked.CustomerID != actor.CustomerID || locked.AccountID != actor.AccountID {
			return ErrNotFound
		}
		if replay, replayErr := s.statusActionReplay(tx, locked, "CUSTOMER", actor.AccountID, command.IdempotencyKey, actionHash); replayErr != nil {
			return replayErr
		} else if replay {
			return nil
		}
		if locked.Status != StatusResolved {
			return ErrInvalidTransition
		}
		return s.transitionWithKey(tx, ctx, locked, StatusClosed, "customer confirmed closure", "CUSTOMER", actor.AccountID, now, map[string]any{"closed_at": &now}, command.IdempotencyKey, actionHash)
	})
	if err != nil {
		if replay, replayErr := s.statusActionReplay(ctx, value, "CUSTOMER", actor.AccountID, command.IdempotencyKey, actionHash); replayErr != nil {
			return nil, replayErr
		} else if replay {
			return s.Get(ctx, actor, publicID)
		}
		return nil, err
	}
	return s.Get(ctx, actor, publicID)
}

func (s *Service) ListForOperator(ctx context.Context, actor OperatorActor, query ListQuery) (pagination.Page[Feedback], error) {
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" {
		return pagination.Page[Feedback]{}, ErrValidation
	}
	query.Status, query.Type = strings.TrimSpace(query.Status), strings.TrimSpace(query.Type)
	if query.Status != "" && !validStatus(Status(query.Status)) || query.Type != "" && !validType(query.Type) {
		return pagination.Page[Feedback]{}, ErrValidation
	}
	if query.Page < 0 || query.Page > maxFeedbackQueryPage || query.PageSize < 0 || query.PageSize > pagination.MaxPageSize {
		return pagination.Page[Feedback]{}, ErrValidation
	}
	query.Page, query.PageSize = pagination.Normalize(query.Page, query.PageSize)
	return s.repo.ListOperator(ctx, actor.TenantID, query)
}

func (s *Service) Process(ctx context.Context, actor OperatorActor, publicID, action string, command ProcessCommand) (*CustomerTimeline, error) {
	command.Content, command.Reason, command.IdempotencyKey = strings.TrimSpace(command.Content), strings.TrimSpace(command.Reason), strings.TrimSpace(command.IdempotencyKey)
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" || !validKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	value, err := s.repo.FindOperator(ctx, actor.TenantID, strings.TrimSpace(publicID))
	if err != nil {
		return nil, err
	}
	actionHash := statusActionHash(value, "OPERATOR", actor.ActorID, action, command.Content, command.Reason)
	if statusAction(action) {
		if replay, replayErr := s.statusActionReplay(ctx, value, "OPERATOR", actor.ActorID, command.IdempotencyKey, actionHash); replayErr != nil {
			return nil, replayErr
		} else if replay {
			return s.timeline(ctx, value)
		}
	}
	now := s.clock.Now().UTC()
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		locked, lockErr := s.repo.FindForUpdate(tx, actor.TenantID, value.ID)
		if lockErr != nil {
			return lockErr
		}
		// 获取聚合锁后重新检查，关闭乐观重放查询与并发提交之间的竞态窗口。
		if statusAction(action) {
			if replay, replayErr := s.statusActionReplay(tx, locked, "OPERATOR", actor.ActorID, command.IdempotencyKey, actionHash); replayErr != nil {
				return replayErr
			} else if replay {
				return nil
			}
		}
		switch action {
		case "accept":
			if locked.Status != StatusSubmitted {
				return ErrInvalidTransition
			}
			return s.transitionWithKey(tx, ctx, locked, StatusAccepted, "accepted", "OPERATOR", actor.ActorID, now, nil, command.IdempotencyKey, actionHash)
		case "respond":
			if !validText(command.Content, 1, 5000) {
				return ErrValidation
			}
			return s.operatorMessage(tx, ctx, locked, actor, command, "CUSTOMER", now)
		case "request-info":
			if !validText(command.Content, 1, 5000) || locked.Status != StatusProcessing {
				return ErrInvalidTransition
			}
			if err := s.operatorMessage(tx, ctx, locked, actor, command, "CUSTOMER", now); err != nil {
				return err
			}
			return s.transitionWithKey(tx, ctx, locked, StatusNeedCustomerInfo, command.Content, "OPERATOR", actor.ActorID, now, nil, command.IdempotencyKey, actionHash)
		case "note":
			if !validText(command.Content, 1, 5000) {
				return ErrValidation
			}
			return s.operatorMessage(tx, ctx, locked, actor, command, "INTERNAL", now)
		case "resolve":
			if !validText(command.Reason, 1, 1000) || locked.Status != StatusProcessing {
				return ErrInvalidTransition
			}
			return s.transitionWithKey(tx, ctx, locked, StatusResolved, command.Reason, "OPERATOR", actor.ActorID, now, map[string]any{"resolved_at": &now}, command.IdempotencyKey, actionHash)
		case "reject":
			if !validText(command.Reason, 1, 1000) || locked.Status != StatusSubmitted && locked.Status != StatusAccepted {
				return ErrInvalidTransition
			}
			return s.transitionWithKey(tx, ctx, locked, StatusRejected, command.Reason, "OPERATOR", actor.ActorID, now, map[string]any{"reject_reason": command.Reason}, command.IdempotencyKey, actionHash)
		default:
			return ErrValidation
		}
	})
	if err != nil {
		if statusAction(action) {
			if replay, replayErr := s.statusActionReplay(ctx, value, "OPERATOR", actor.ActorID, command.IdempotencyKey, actionHash); replayErr != nil {
				return nil, replayErr
			} else if replay {
				updated, readErr := s.repo.FindOperator(ctx, actor.TenantID, publicID)
				if readErr != nil {
					return nil, readErr
				}
				return s.timeline(ctx, updated)
			}
		} else {
			visibility := "CUSTOMER"
			if action == "note" {
				visibility = "INTERNAL"
			}
			expectedHash := payloadHash(command.Content, visibility)
			if existing, findErr := s.repo.FindMessageByKey(ctx, actor.TenantID, value.ID, "OPERATOR", actor.ActorID, command.IdempotencyKey); findErr == nil {
				if existing.RequestHash != expectedHash {
					return nil, ErrIdempotencyConflict
				}
				updated, readErr := s.repo.FindOperator(ctx, actor.TenantID, publicID)
				if readErr != nil {
					return nil, readErr
				}
				return s.timeline(ctx, updated)
			}
		}
		return nil, err
	}
	updated, err := s.repo.FindOperator(ctx, actor.TenantID, publicID)
	if err != nil {
		return nil, err
	}
	return s.timeline(ctx, updated)
}

func (s *Service) operatorMessage(tx, ctx context.Context, value *Feedback, actor OperatorActor, command ProcessCommand, visibility string, now time.Time) error {
	if value.Status != StatusAccepted && value.Status != StatusProcessing && value.Status != StatusNeedCustomerInfo {
		return ErrInvalidTransition
	}
	hash := payloadHash(command.Content, visibility)
	if existing, err := s.repo.FindMessageByKey(tx, actor.TenantID, value.ID, "OPERATOR", actor.ActorID, command.IdempotencyKey); err == nil {
		if existing.RequestHash != hash {
			return ErrIdempotencyConflict
		}
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := s.repo.CreateMessage(tx, &Message{TenantID: actor.TenantID, FeedbackID: value.ID, SenderType: "OPERATOR", SenderID: actor.ActorID, Content: command.Content, Visibility: visibility, IdempotencyKey: command.IdempotencyKey, RequestHash: hash, CreatedAt: now}); err != nil {
		return err
	}
	if visibility == "CUSTOMER" {
		fields := map[string]any{}
		if value.FirstRespondedAt == nil {
			fields["first_responded_at"] = &now
			value.FirstRespondedAt = &now
		}
		if value.Status == StatusAccepted {
			if err := s.transition(tx, ctx, value, StatusProcessing, "first operator response", "OPERATOR", actor.ActorID, now, fields); err != nil {
				return err
			}
		} else if len(fields) > 0 {
			if err := s.repo.Update(tx, value, value.Version, fields); err != nil {
				return err
			}
			value.Version++
		}
	}
	if visibility == "CUSTOMER" {
		if repo, ok := s.repo.(customerNotificationRepository); ok {
			if err := repo.CreateNotification(tx, &Notification{TenantID: value.TenantID, AccountID: value.AccountID, FeedbackID: value.ID, EventID: s.ids.NewID(), Kind: "FEEDBACK_OPERATOR_MESSAGE", Title: "客户反馈有新的服务回复", Body: truncateNotification(command.Content), TargetPath: "/customer-portal/feedback", Status: "UNREAD", CreatedAt: now}); err != nil {
				return err
			}
		}
	}
	return s.emit(tx, value, "PORTAL_FEEDBACK_OPERATOR_MESSAGE", now)
}

func (s *Service) timeline(ctx context.Context, value *Feedback) (*CustomerTimeline, error) {
	messages, err := s.repo.ListCustomerMessages(ctx, value.TenantID, value.ID)
	if err != nil {
		return nil, err
	}
	logs, err := s.repo.ListStatusLogs(ctx, value.TenantID, value.ID)
	if err != nil {
		return nil, err
	}
	return customerTimeline(value, messages, logs), nil
}

func (s *Service) transition(tx, ctx context.Context, value *Feedback, to Status, reason, actorType, actorID string, now time.Time, fields map[string]any) error {
	return s.transitionWithKey(tx, ctx, value, to, reason, actorType, actorID, now, fields, "", "")
}

func (s *Service) transitionWithKey(tx, ctx context.Context, value *Feedback, to Status, reason, actorType, actorID string, now time.Time, fields map[string]any, idempotencyKey, requestHash string) error {
	from := value.Status
	if fields == nil {
		fields = map[string]any{}
	}
	fields["status"], fields["updated_by"], fields["updated_at"] = to, actorID, now
	if err := s.repo.Update(tx, value, value.Version, fields); err != nil {
		return err
	}
	value.Status, value.Version = to, value.Version+1
	log := newStatusLog(ctx, value, actorType, actorID, from, to, reason, now)
	if idempotencyKey != "" {
		log.IdempotencyKey = &idempotencyKey
		log.RequestHash = requestHash
	}
	if err := s.repo.CreateStatusLog(tx, log); err != nil {
		return err
	}
	if actorType == "OPERATOR" {
		if repo, ok := s.repo.(customerNotificationRepository); ok {
			if err := repo.CreateNotification(tx, &Notification{TenantID: value.TenantID, AccountID: value.AccountID, FeedbackID: value.ID, EventID: s.ids.NewID(), Kind: "FEEDBACK_STATUS_CHANGED", Title: "客户反馈状态已更新", Body: "反馈单 " + value.FeedbackNo + " 当前状态：" + string(to), TargetPath: "/customer-portal/feedback", Status: "UNREAD", CreatedAt: now}); err != nil {
				return err
			}
		}
	}
	return s.emit(tx, value, "PORTAL_FEEDBACK_STATUS_CHANGED", now)
}

func truncateNotification(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= 500 {
		return value
	}
	runes := []rune(value)
	return string(runes[:500])
}

func (s *Service) statusActionReplay(ctx context.Context, value *Feedback, actorType, actorID, key, expectedHash string) (bool, error) {
	existing, err := s.repo.FindStatusActionByKey(ctx, value.TenantID, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existing.FeedbackID != value.ID || existing.ActorType != actorType || existing.ActorID != actorID || existing.RequestHash != expectedHash {
		return false, ErrIdempotencyConflict
	}
	return true, nil
}

func (s *Service) emit(ctx context.Context, value *Feedback, eventType string, now time.Time) error {
	payload, _ := json.Marshal(map[string]any{"feedback_id": value.PublicID, "feedback_no": value.FeedbackNo, "status": value.Status})
	return s.repo.CreateOutbox(ctx, &Outbox{EventID: s.ids.NewID(), TenantID: value.TenantID, EventType: eventType, AggregateID: value.ID, Payload: payload, Status: "PENDING", CreatedAt: now})
}

func newStatusLog(ctx context.Context, value *Feedback, actorType, actorID string, from, to Status, reason string, now time.Time) *StatusLog {
	return &StatusLog{TenantID: value.TenantID, FeedbackID: value.ID, FromStatus: from, ToStatus: to, Reason: reason, ActorType: actorType, ActorID: actorID, RequestID: requestctx.ID(ctx), OccurredAt: now}
}
func normalizeCreate(c *CreateCommand) {
	c.Type = strings.ToUpper(strings.TrimSpace(c.Type))
	c.Title = strings.TrimSpace(c.Title)
	c.Description = strings.TrimSpace(c.Description)
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	c.ExpectedContact = strings.TrimSpace(c.ExpectedContact)
	c.IdempotencyKey = strings.TrimSpace(c.IdempotencyKey)
}
func validCustomer(a CustomerActor) bool {
	return strings.TrimSpace(a.TenantID) != "" && strings.TrimSpace(a.AccountID) != "" && a.CustomerID > 0
}
func validCreate(c CreateCommand) bool {
	return validType(c.Type) && validText(c.Title, 1, 200) && validText(c.Description, 1, 5000) && validText(c.ProjectID, 0, 64) && validText(c.ExpectedContact, 0, 200) && validKey(c.IdempotencyKey)
}
func validType(v string) bool { return v == "OBJECTION" || v == "COMPLAINT" || v == "SUGGESTION" }
func validStatus(v Status) bool {
	switch v {
	case StatusSubmitted, StatusAccepted, StatusProcessing, StatusNeedCustomerInfo, StatusResolved, StatusClosed, StatusRejected:
		return true
	}
	return false
}
func statusAction(action string) bool {
	return action == "accept" || action == "request-info" || action == "resolve" || action == "reject"
}
func validText(v string, min, max int) bool {
	n := utf8.RuneCountInString(v)
	return n >= min && n <= max && utf8.ValidString(v)
}
func validKey(v string) bool { return validText(v, 8, 128) && !strings.ContainsAny(v, "\r\n\t") }
func invalidGeneratedID(v string) bool {
	return strings.TrimSpace(v) == "" || v == "request-id-unavailable" || len(v) > 64
}
func payloadHash(values ...string) string {
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// statusActionHash 将持久化键绑定到完整客户边界、聚合、操作者、动作及规范载荷。
// statusActionReplay 的独立字段比较让冲突判断对调用方保持不透明。
func statusActionHash(value *Feedback, actorType, actorID, action, content, reason string) string {
	return payloadHash(
		"portal-feedback-status-action-v1",
		value.TenantID,
		strconv.FormatUint(value.CustomerID, 10),
		value.AccountID,
		value.PublicID,
		actorType,
		actorID,
		strings.TrimSpace(action),
		strings.TrimSpace(content),
		strings.TrimSpace(reason),
	)
}
