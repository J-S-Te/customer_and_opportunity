package opportunity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
)

type ContractVerifier interface {
	BelongsToCustomer(context.Context, string, uint64) (bool, error)
}

func (s *Service) Board(ctx context.Context, query ListQuery) ([]BoardColumn, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	query, err = validateBoardQuery(query)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.Board(ctx, principal, query)
	if err != nil {
		return nil, err
	}
	s.enrichSignedContractCounts(ctx, items)
	return groupBoard(items), nil
}

func groupBoard(items []Response) []BoardColumn {
	stages := []string{StageInitial, StageRequirement, StageSolution, StageQuotation, StageBid, StageSigned, StageFailed}
	columns := make([]BoardColumn, 0, len(stages))
	byStage := make(map[string][]Response, len(stages))
	for _, item := range items {
		byStage[item.CurrentStage] = append(byStage[item.CurrentStage], item)
	}
	for _, stage := range stages {
		values := byStage[stage]
		if values == nil {
			values = []Response{}
		}
		columns = append(columns, BoardColumn{Stage: stage, Items: values})
	}
	return columns
}

func (s *Service) StageHistory(ctx context.Context, id uint64, page, pageSize int) (pagination.Page[StageHistoryResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return pagination.Page[StageHistoryResponse]{}, err
	}
	if err = validatePagination(page, pageSize); err != nil {
		return pagination.Page[StageHistoryResponse]{}, err
	}
	if _, err = s.repo.FindByID(ctx, principal, id); err != nil {
		return pagination.Page[StageHistoryResponse]{}, err
	}
	return s.repo.StageHistory(ctx, principal.TenantID, id, page, pageSize)
}

func (s *Service) CreateFollowup(ctx context.Context, id uint64, input FollowupCreateRequest) (*FollowupResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	modelOpportunity, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if modelOpportunity.Status == StatusVoid {
		return nil, ErrInactive
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_FOLLOWUP_CONTENT_REQUIRED", "followup content is required")
	}
	model := &Followup{OpportunityID: id, Type: input.Type, Content: html.EscapeString(content), FollowedAt: input.FollowedAt.UTC(), FollowedBy: principal.UserID, NextFollowAt: input.NextFollowAt}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, principal.UserID, principal.UserID, 1
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if createErr := s.repo.CreateFollowup(txCtx, model); createErr != nil {
			return createErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "FOLLOWUP_CREATE", ResourceType: "opportunity", ResourceID: uintString(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(toFollowupResponse(model)), Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	result := toFollowupResponse(model)
	return &result, nil
}

func (s *Service) ListFollowups(ctx context.Context, id uint64, page, pageSize int) (pagination.Page[FollowupResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	if err = validatePagination(page, pageSize); err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	if _, err = s.repo.FindByID(ctx, principal, id); err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	return s.repo.ListFollowups(ctx, principal.TenantID, id, page, pageSize)
}

func (s *Service) CompleteTerminalTodo(ctx context.Context, id uint64, input TerminalTodoRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if model.Version != input.Version {
		return nil, ErrVersionConflict
	}
	if model.Status == StatusVoid {
		return nil, ErrInactive
	}
	before := toResponse(model)
	switch model.TerminalPendingType {
	case PendingContract:
		if model.CurrentStage != StageSigned || input.ContractRef == nil || strings.TrimSpace(*input.ContractRef) == "" {
			return nil, ErrTerminalTodoAbsent
		}
		if s.contracts == nil {
			return nil, apperror.New(503, "INTEGRATION_CONTRACT_NOT_CONFIGURED", "contract verification is not configured")
		}
		belongs, verifyErr := s.contracts.BelongsToCustomer(ctx, strings.TrimSpace(*input.ContractRef), model.CustomerID)
		if verifyErr != nil {
			return nil, apperror.Wrap(verifyErr, 503, "INTEGRATION_DEPENDENCY_UNAVAILABLE", "contract verification is unavailable")
		}
		if !belongs {
			return nil, apperror.New(422, "CRM_OPPORTUNITY_CONTRACT_CUSTOMER_MISMATCH", "contract belongs to another customer")
		}
		contract := strings.TrimSpace(*input.ContractRef)
		model.ContractRef, model.TerminalPendingType = &contract, PendingNone
	case PendingLostReason:
		if model.CurrentStage != StageFailed || !validLostReason(input.LostReason) {
			return nil, ErrLostReasonRequired
		}
		reason := strings.TrimSpace(*input.LostReason)
		model.LostReason, model.TerminalPendingType = &reason, PendingNone
	default:
		return nil, ErrTerminalTodoAbsent
	}
	model.UpdatedBy = principal.UserID
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if updateErr := s.repo.UpdateTerminalTodo(txCtx, model, input.Version); updateErr != nil {
			return updateErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "TERMINAL_TODO_COMPLETE", ResourceType: "opportunity", ResourceID: uintString(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(toResponse(model)), Reason: input.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

type Service struct {
	db              *gorm.DB
	repo            Repository
	audit           audit.Writer
	contracts       ContractVerifier
	signedContracts SignedContractCounter
	qbStatuses      QBStatusReader
	launches        *ExternalLaunchSigner
	owners          ownerdirectory.Catalog
	now             func() time.Time
	// 仅在服务单元测试中注入；生产构造始终使用共享 GORM 事务边界。
	createTransaction func(context.Context, func(context.Context) error) error
	// 转合同命令保持与生产相同的事务语义，同时让聚焦测试无需连接真实 MySQL。
	contractTransferTransaction func(context.Context, func(context.Context) error) error
}

func (s *Service) UseQBStatusReader(reader QBStatusReader) *Service {
	s.qbStatuses = reader
	return s
}

func (s *Service) UseSignedContractCounter(counter SignedContractCounter) *Service {
	s.signedContracts = counter
	return s
}

func (s *Service) UseExternalLaunchSigner(signer *ExternalLaunchSigner) *Service {
	s.launches = signer
	return s
}

func (s *Service) UseOwnerDirectory(catalog ownerdirectory.Catalog) *Service {
	s.owners = catalog
	return s
}

func NewService(db *gorm.DB, repo Repository, auditWriter audit.Writer, contracts ContractVerifier) *Service {
	return &Service{
		db: db, repo: repo, audit: auditWriter, contracts: contracts,
		now: func() time.Time { return time.Now().UTC() },
		createTransaction: func(ctx context.Context, fn func(context.Context) error) error {
			return database.WithTransaction(ctx, db, fn)
		},
		contractTransferTransaction: func(ctx context.Context, fn func(context.Context) error) error {
			return database.WithTransaction(ctx, db, fn)
		},
	}
}

func (s *Service) Create(ctx context.Context, input CreateRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return nil, ErrIdempotencyRequired
	}
	if len(key) > 128 {
		return nil, ErrIdempotencyKeyTooLong
	}
	input = inheritCreateOwner(normalizeCreateRequest(input), principal)
	input.IdempotencyKey = key
	if s.owners != nil {
		if input.OwnerOrgID == "" {
			page, listErr := s.owners.List(ctx, ownerdirectory.Query{UserID: input.OwnerUserID, Page: 1, PageSize: 1})
			if listErr != nil {
				return nil, listErr
			}
			input.OwnerOrgID = ownerdirectory.PrimaryOrganization(page, input.OwnerUserID)
		}
		if err = s.owners.Validate(ctx, input.OwnerUserID, input.OwnerOrgID); err != nil {
			return nil, err
		}
	}
	amount, err := decimal.NewFromString(input.ExpectedAmount)
	if err != nil || !amount.IsPositive() {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_INVALID_AMOUNT", "expected amount must be greater than zero")
	}
	signDate, err := time.Parse("2006-01-02", input.ExpectedSignDate)
	if err != nil || signDate.Before(s.now().Truncate(24*time.Hour)) {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_INVALID_SIGN_DATE", "expected sign date must not be earlier than today")
	}
	requestHash, err := createRequestHash(input)
	if err != nil {
		return nil, err
	}
	model := &Opportunity{Name: input.Name, CustomerID: input.CustomerID, Type: input.Type, Source: input.Source, ExpectedAmount: amount, ExpectedSignDate: signDate, RequirementSummary: input.RequirementSummary, SystemCount: input.SystemCount, PainPoints: input.PainPoints, CompetitorInfo: input.CompetitorInfo, OwnerUserID: input.OwnerUserID, OwnerOrgID: input.OwnerOrgID, CurrentStage: StageInitial, Status: StatusFollowing, TerminalPendingType: PendingNone, StageChangedAt: s.now()}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, principal.UserID, principal.UserID, 1
	err = s.runCreateTransaction(ctx, func(txCtx context.Context) error {
		// 先证明客户授权范围，再查询重放记录，避免调用方通过猜测幂等键探测已停用或范围外客户。
		visible, visibleErr := s.repo.CustomerVisible(txCtx, principal, input.CustomerID)
		if visibleErr != nil {
			return visibleErr
		}
		if !visible {
			return ErrCustomerForbidden
		}
		prior, replayErr := s.repo.FindCreateIdempotency(txCtx, principal.TenantID, principal.UserID, key)
		if replayErr != nil {
			return replayErr
		}
		if prior != nil {
			replayed, replayErr := s.loadCreateReplay(txCtx, principal, input.CustomerID, key, requestHash, prior)
			if replayErr != nil {
				return replayErr
			}
			return createReplayResult(txCtx, replayed)
		}
		model.OpportunityNo, err = s.repo.NextNumber(txCtx, principal.TenantID, s.now().Format("20060102"))
		if err != nil {
			return err
		}
		if err = s.repo.Create(txCtx, model); err != nil {
			return err
		}
		createdResponse := toResponse(model)
		responseJSON, encodeErr := json.Marshal(createdResponse)
		if encodeErr != nil {
			return encodeErr
		}
		if err = s.repo.CreateCreateIdempotency(txCtx, &CreateIdempotency{
			TenantID: principal.TenantID, ActorID: principal.UserID, Key: key,
			CustomerID: input.CustomerID, OpportunityID: model.ID,
			RequestHash: requestHash, ResponseHash: bytesHash(responseJSON), ResponseJSON: responseJSON,
			RequestIDTrace: request.ID(ctx), CreatedAt: s.now(),
		}); err != nil {
			return err
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "CREATE", ResourceType: "opportunity", ResourceID: uintString(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(toResponse(model)), Result: "SUCCESS"})
	})
	if err != nil {
		var replayErr *replayResultError
		if errors.As(err, &replayErr) {
			return replayErr.Response, nil
		}
		if isDuplicateCreateError(err) {
			return s.resolveCreateRace(ctx, principal, input.CustomerID, key, requestHash)
		}
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

func (s *Service) runCreateTransaction(ctx context.Context, fn func(context.Context) error) error {
	if s.createTransaction != nil {
		return s.createTransaction(ctx, fn)
	}
	return database.WithTransaction(ctx, s.db, fn)
}

func normalizeCreateRequest(input CreateRequest) CreateRequest {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.TrimSpace(input.Type)
	input.Source = strings.TrimSpace(input.Source)
	input.ExpectedAmount = strings.TrimSpace(input.ExpectedAmount)
	input.ExpectedSignDate = strings.TrimSpace(input.ExpectedSignDate)
	input.RequirementSummary = strings.TrimSpace(input.RequirementSummary)
	input.PainPoints = strings.TrimSpace(input.PainPoints)
	input.CompetitorInfo = strings.TrimSpace(input.CompetitorInfo)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OwnerOrgID = strings.TrimSpace(input.OwnerOrgID)
	return input
}

// inheritCreateOwner 使用可信认证主体确定新商机的初始负责人；客户端兼容字段不能覆盖归属。
func inheritCreateOwner(input CreateRequest, principal auth.Principal) CreateRequest {
	input.OwnerUserID = strings.TrimSpace(principal.UserID)
	input.OwnerOrgID = strings.TrimSpace(principal.PrimaryOrgID)
	return input
}

func createRequestHash(input CreateRequest) (string, error) {
	canonical := struct {
		Name, Type, Source, ExpectedAmount, ExpectedSignDate string
		RequirementSummary, PainPoints, CompetitorInfo       string
		CustomerID, SystemCount                              uint64
		OwnerUserID, OwnerOrgID                              string
	}{
		Name: input.Name, Type: input.Type, Source: input.Source,
		ExpectedAmount: input.ExpectedAmount, ExpectedSignDate: input.ExpectedSignDate,
		RequirementSummary: input.RequirementSummary, PainPoints: input.PainPoints,
		CompetitorInfo: input.CompetitorInfo, CustomerID: input.CustomerID,
		SystemCount: uint64(input.SystemCount), OwnerUserID: input.OwnerUserID, OwnerOrgID: input.OwnerOrgID,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func bytesHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validateCreateReplay(value *CreateIdempotency, principal auth.Principal, customerID uint64, key, requestHash string) error {
	if value == nil || value.TenantID != principal.TenantID || value.ActorID != principal.UserID ||
		value.Key != key || value.CustomerID != customerID || value.OpportunityID == 0 ||
		value.RequestHash != requestHash || len(value.ResponseHash) != 64 {
		return ErrIdempotencyConflict
	}
	return nil
}

func (s *Service) loadCreateReplay(ctx context.Context, principal auth.Principal, customerID uint64, key, requestHash string, replay *CreateIdempotency) (*Response, error) {
	if err := validateCreateReplay(replay, principal, customerID, key, requestHash); err != nil {
		return nil, err
	}
	model, err := s.repo.FindCreatedByID(ctx, principal.TenantID, replay.OpportunityID)
	if err != nil {
		return nil, err
	}
	if model.CreatedBy != principal.UserID || model.CustomerID != customerID {
		return nil, ErrIdempotencyConflict
	}
	if bytesHash(replay.ResponseJSON) != replay.ResponseHash {
		return nil, ErrIdempotencyConflict
	}
	var result Response
	if err = json.Unmarshal(replay.ResponseJSON, &result); err != nil {
		return nil, ErrIdempotencyConflict
	}
	if result.ID != replay.OpportunityID || result.CustomerID != customerID {
		return nil, ErrIdempotencyConflict
	}
	return &result, nil
}

func (s *Service) resolveCreateRace(ctx context.Context, principal auth.Principal, customerID uint64, key, requestHash string) (*Response, error) {
	var replayed *Response
	err := s.runCreateTransaction(ctx, func(txCtx context.Context) error {
		visible, visibleErr := s.repo.CustomerVisible(txCtx, principal, customerID)
		if visibleErr != nil {
			return visibleErr
		}
		if !visible {
			return ErrCustomerForbidden
		}
		replay, replayErr := s.repo.FindCreateIdempotency(txCtx, principal.TenantID, principal.UserID, key)
		if replayErr != nil {
			return replayErr
		}
		if replay == nil {
			return ErrIdempotencyConflict
		}
		replayed, replayErr = s.loadCreateReplay(txCtx, principal, customerID, key, requestHash, replay)
		return replayErr
	})
	if err != nil {
		return nil, err
	}
	return replayed, nil
}

// replayResultError 用于终止当前事务且不提交新工作，同时把此前已提交的重放响应带回调用方。
type replayResultError struct{ Response *Response }

func (e *replayResultError) Error() string { return "opportunity creation replay" }

func createReplayResult(_ context.Context, response *Response) error {
	return &replayResultError{Response: response}
}

func isDuplicateCreateError(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	// 商机编号或其他业务唯一键冲突不等于幂等并发。只对明确的重放坐标索引做恢复；
	// 通用重复错误已经丢失关键证据，不能安全地当成同请求胜出记录。
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 &&
		strings.Contains(mysqlErr.Message, "uq_opportunity_create_idem")
}

func (s *Service) Update(ctx context.Context, id uint64, input UpdateRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	amount, signDate, err := validateMasterData(input.ExpectedAmount, input.ExpectedSignDate)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if model.Status == StatusVoid {
		return nil, ErrInactive
	}
	if !model.CreatedAt.IsZero() && signDate.Before(dateOnly(model.CreatedAt)) {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_INVALID_SIGN_DATE", "expected sign date must not be earlier than opportunity creation date")
	}
	if model.Version != input.Version {
		return nil, ErrVersionConflict
	}
	if strings.TrimSpace(input.Reason) == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_CHANGE_REASON_REQUIRED", "change reason is required")
	}
	before := toResponse(model)
	model.Name, model.Type, model.Source = strings.TrimSpace(input.Name), strings.TrimSpace(input.Type), strings.TrimSpace(input.Source)
	model.ExpectedAmount, model.ExpectedSignDate = amount, signDate
	model.RequirementSummary, model.SystemCount = strings.TrimSpace(input.RequirementSummary), input.SystemCount
	model.PainPoints, model.CompetitorInfo, model.UpdatedBy = strings.TrimSpace(input.PainPoints), strings.TrimSpace(input.CompetitorInfo), principal.UserID
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if updateErr := s.repo.Update(txCtx, model, input.Version); updateErr != nil {
			return updateErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "UPDATE", ResourceType: "opportunity", ResourceID: uintString(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(toResponse(model)), Reason: input.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

func validateMasterData(expectedAmount, expectedSignDate string) (decimal.Decimal, time.Time, error) {
	amount, err := decimal.NewFromString(expectedAmount)
	if err != nil || !amount.IsPositive() {
		return decimal.Zero, time.Time{}, apperror.New(422, "CRM_OPPORTUNITY_INVALID_AMOUNT", "expected amount must be greater than zero")
	}
	signDate, err := time.Parse("2006-01-02", expectedSignDate)
	if err != nil {
		return decimal.Zero, time.Time{}, apperror.New(422, "CRM_OPPORTUNITY_INVALID_SIGN_DATE", "expected sign date is invalid")
	}
	return amount, signDate, nil
}

func dateOnly(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Service) Void(ctx context.Context, id uint64, input LifecycleRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if model.Status == StatusVoid {
		return nil, ErrInactive
	}
	if model.Version != input.Version {
		return nil, ErrVersionConflict
	}
	if strings.TrimSpace(input.Reason) == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_CHANGE_REASON_REQUIRED", "change reason is required")
	}
	blockers, err := s.repo.VoidBlockers(ctx, principal.TenantID, id)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		return nil, apperror.WithDetails(ErrVoidBlocked, map[string]any{"blockers": blockers})
	}
	before := toResponse(model)
	previousStatus, now := model.Status, s.now()
	model.Status, model.StatusBeforeVoid, model.EndDate, model.UpdatedBy = StatusVoid, &previousStatus, &now, principal.UserID
	if err = s.persistLifecycle(ctx, principal, model, before, input, previousStatus, "VOID"); err != nil {
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

func (s *Service) Restore(ctx context.Context, id uint64, input LifecycleRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if model.Status != StatusVoid {
		return nil, ErrNotVoid
	}
	if model.Version != input.Version {
		return nil, ErrVersionConflict
	}
	if strings.TrimSpace(input.Reason) == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_CHANGE_REASON_REQUIRED", "change reason is required")
	}
	before := toResponse(model)
	restoredStatus := StatusFollowing
	if model.StatusBeforeVoid != nil && (*model.StatusBeforeVoid == StatusFollowing || *model.StatusBeforeVoid == StatusClosed) {
		restoredStatus = *model.StatusBeforeVoid
	}
	model.Status, model.StatusBeforeVoid, model.EndDate, model.UpdatedBy = restoredStatus, nil, nil, principal.UserID
	if err = s.persistLifecycle(ctx, principal, model, before, input, StatusVoid, "RESTORE"); err != nil {
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

func (s *Service) persistLifecycle(ctx context.Context, principal auth.Principal, model *Opportunity, before Response, input LifecycleRequest, expectedStatus, operation string) error {
	return database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if err := s.repo.UpdateLifecycle(txCtx, model, input.Version, expectedStatus); err != nil {
			return err
		}
		if operation == "VOID" {
			if err := cancelActiveStageAlerts(txCtx, s.db, principal.TenantID, model.ID, principal.UserID, s.now()); err != nil {
				return err
			}
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: operation, ResourceType: "opportunity", ResourceID: uintString(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(toResponse(model)), Reason: input.Reason, Result: "SUCCESS"})
	})
}

func (s *Service) Get(ctx context.Context, id uint64) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	result := toResponse(model)
	members, err := s.repo.ListMembers(ctx, principal.TenantID, id, false)
	if err != nil {
		return nil, err
	}
	result.Members = memberResponses(members)
	values := []Response{result}
	s.enrichSignedContractCounts(ctx, values)
	result = values[0]
	return &result, nil
}

func (s *Service) List(ctx context.Context, query ListQuery) (pagination.Page[Response], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return pagination.Page[Response]{}, err
	}
	query, err = validateListQuery(query)
	if err != nil {
		return pagination.Page[Response]{}, err
	}
	result, err := s.repo.List(ctx, principal, query)
	if err != nil {
		return pagination.Page[Response]{}, err
	}
	s.enrichSignedContractCounts(ctx, result.Items)
	return result, nil
}

func (s *Service) enrichSignedContractCounts(ctx context.Context, items []Response) {
	if s.signedContracts == nil || len(items) == 0 {
		return
	}
	ids := make([]uint64, 0, len(items))
	seen := make(map[uint64]struct{}, len(items))
	for i := range items {
		if items[i].ID == 0 {
			continue
		}
		if _, duplicate := seen[items[i].ID]; duplicate {
			continue
		}
		seen[items[i].ID] = struct{}{}
		ids = append(ids, items[i].ID)
	}
	if len(ids) == 0 {
		return
	}
	counts, err := s.signedContracts.CountSignedContracts(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "signed contract count lookup failed", "module", "opportunity", "error", err)
		return
	}
	for _, id := range ids {
		if _, ok := counts[id]; !ok {
			slog.WarnContext(ctx, "signed contract count response omitted opportunity", "module", "opportunity")
			return
		}
	}
	for i := range items {
		count := counts[items[i].ID]
		value := count
		items[i].SignedContractCount = &value
	}
}

func validateListQuery(query ListQuery) (ListQuery, error) {
	query.Keyword = strings.TrimSpace(query.Keyword)
	query.Stage = strings.TrimSpace(query.Stage)
	query.Status = strings.ToUpper(strings.TrimSpace(query.Status))
	query.OwnerID = strings.TrimSpace(query.OwnerID)
	query.SortBy = strings.ToLower(strings.TrimSpace(query.SortBy))
	query.SortOrder = strings.ToLower(strings.TrimSpace(query.SortOrder))
	if len([]rune(query.Keyword)) > 200 || len(query.OwnerID) > 64 || validatePagination(query.Page, query.PageSize) != nil {
		return ListQuery{}, ErrInvalidQuery
	}
	if query.Stage != "" && !isStage(query.Stage) {
		return ListQuery{}, ErrInvalidQuery
	}
	allowedStatus := map[string]bool{"": true, StatusFollowing: true, StatusClosed: true, StatusVoid: true}
	allowedSort := map[string]bool{"": true, "created_at": true, "updated_at": true, "expected_amount": true, "expected_sign_date": true}
	if !allowedStatus[query.Status] || !allowedSort[query.SortBy] || (query.SortOrder != "" && query.SortOrder != "asc" && query.SortOrder != "desc") {
		return ListQuery{}, ErrInvalidQuery
	}
	return query, nil
}

func validateBoardQuery(query ListQuery) (ListQuery, error) {
	query.Keyword = strings.TrimSpace(query.Keyword)
	query.OwnerID = strings.TrimSpace(query.OwnerID)
	if len([]rune(query.Keyword)) > 200 || len(query.OwnerID) > 64 || query.Stage != "" || query.Status != "" || query.Page != 0 || query.PageSize != 0 || query.SortBy != "" || query.SortOrder != "" {
		return ListQuery{}, ErrInvalidQuery
	}
	return query, nil
}

func validatePagination(page, pageSize int) error {
	if page < 1 || page > maxQueryPage || pageSize < 1 || pageSize > pagination.MaxPageSize {
		return ErrInvalidQuery
	}
	return nil
}

func (s *Service) ChangeStage(ctx context.Context, id uint64, input StageChangeRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !isStage(input.TargetStage) {
		return nil, ErrInvalidStage
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if model.Version != input.Version {
		return nil, ErrVersionConflict
	}
	if model.Status == StatusVoid {
		return nil, ErrInactive
	}
	if input.TargetStage == StageSigned {
		if input.ContractRef == nil || strings.TrimSpace(*input.ContractRef) == "" {
			return nil, ErrContractRequired
		}
		if s.contracts == nil {
			return nil, apperror.New(503, "INTEGRATION_CONTRACT_NOT_CONFIGURED", "contract verification is not configured")
		}
		belongs, verifyErr := s.contracts.BelongsToCustomer(ctx, *input.ContractRef, model.CustomerID)
		if verifyErr != nil {
			return nil, apperror.Wrap(verifyErr, 503, "INTEGRATION_DEPENDENCY_UNAVAILABLE", "contract verification is unavailable")
		}
		if !belongs {
			return nil, apperror.New(422, "CRM_OPPORTUNITY_CONTRACT_CUSTOMER_MISMATCH", "contract belongs to another customer")
		}
	}
	if input.TargetStage == StageFailed && !validLostReason(input.LostReason) {
		return nil, ErrLostReasonRequired
	}
	before := toResponse(model)
	applyStage(model, input.TargetStage, input.ContractRef, input.LostReason, PendingNone, s.now(), principal.UserID)
	err = s.persistStage(ctx, principal, model, before, input.Version, StageLog{TenantID: principal.TenantID, OpportunityID: model.ID, FromStage: before.CurrentStage, ToStage: model.CurrentStage, Source: SourceManual, SourceID: request.ID(ctx), Reason: input.Reason, ContractRef: model.ContractRef, LostReason: model.LostReason, PendingType: model.TerminalPendingType, OperatorID: principal.UserID, ChangedAt: model.StageChangedAt, RequestID: request.ID(ctx)})
	if err != nil {
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

func (s *Service) ApplyExternalStatus(ctx context.Context, input ExternalStatusRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	input, amount, err := normalizeExternalStatus(input)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, auth.Principal{UserID: principal.UserID, TenantID: principal.TenantID, DisplayName: principal.DisplayName, ScopeMode: auth.ScopeAll, Permissions: principal.Permissions}, input.OpportunityID)
	if err != nil {
		return nil, err
	}
	if model.Status == StatusVoid {
		return nil, ErrInactive
	}
	prior, err := s.repo.FindExternalLink(ctx, principal.TenantID, input.OpportunityID, input.SourceID, input.Status)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if validateExternalReplay(prior, input, amount) != nil {
			return nil, ErrIdempotencyConflict
		}
		result := toResponse(model)
		return &result, nil
	}
	if input.ChangedAt.Before(model.StageChangedAt) || (model.ExternalStatusChangedAt != nil && input.ChangedAt.Before(*model.ExternalStatusChangedAt)) {
		if auditErr := s.audit.Write(ctx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "EXTERNAL_STATUS_STALE", ResourceType: "opportunity", ResourceID: uintString(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(toResponse(model)), AfterJSON: audit.JSON(struct {
			Type      string    `json:"type"`
			SourceID  string    `json:"source_id"`
			Status    string    `json:"status"`
			ChangedAt time.Time `json:"changed_at"`
		}{input.Type, input.SourceID, input.Status, input.ChangedAt}), Result: "STALE"}); auditErr != nil {
			return nil, auditErr
		}
		return nil, ErrStaleEvent
	}
	target, pending := mapExternalStatus(input.Status, input.ContractRef, input.LostReason)
	if target == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_UNKNOWN_EXTERNAL_STATUS", "unsupported external status")
	}
	before := toResponse(model)
	applyStage(model, target, input.ContractRef, input.LostReason, pending, input.ChangedAt.UTC(), principal.UserID)
	model.ExternalStatusChangedAt = pointerTime(input.ChangedAt.UTC())
	log := StageLog{TenantID: principal.TenantID, OpportunityID: model.ID, FromStage: before.CurrentStage, ToStage: model.CurrentStage, Source: SourceQBCallback, SourceID: externalStageSourceID(input.SourceID, input.Status), ContractRef: model.ContractRef, LostReason: model.LostReason, PendingType: model.TerminalPendingType, OperatorID: principal.UserID, ChangedAt: model.StageChangedAt, RequestID: request.ID(ctx)}
	snapshot, err := externalLinkSnapshot(principal.TenantID, model.ID, input, amount, s.now())
	if err != nil {
		return nil, err
	}
	if err = s.persistExternalStage(ctx, principal, model, before, model.Version, log, snapshot); err != nil {
		// 相同载荷的回调可能在双方看到持久化快照前并发；阶段乐观锁负责串行写入，
		// 失败方只能恢复与数据库精确绑定的重放结果。
		winner, findErr := s.repo.FindExternalLink(ctx, principal.TenantID, input.OpportunityID, input.SourceID, input.Status)
		if findErr == nil && winner != nil {
			if validateExternalReplay(winner, input, amount) != nil {
				return nil, ErrIdempotencyConflict
			}
			current, currentErr := s.repo.FindByID(ctx, auth.Principal{UserID: principal.UserID, TenantID: principal.TenantID, ScopeMode: auth.ScopeAll}, input.OpportunityID)
			if currentErr == nil {
				result := toResponse(current)
				return &result, nil
			}
		}
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

func validateExternalReplay(prior *ExternalLink, input ExternalStatusRequest, amount *decimal.Decimal) error {
	expected, err := externalLinkSnapshot(prior.TenantID, prior.OpportunityID, input, amount, prior.CreatedAt)
	if err != nil || prior.Type != expected.Type || !prior.ChangedAt.Equal(expected.ChangedAt) || bytesHash(prior.SnapshotJSON) != bytesHash(expected.SnapshotJSON) {
		return ErrIdempotencyConflict
	}
	return nil
}

func externalStageSourceID(sourceID, status string) string {
	sum := sha256.Sum256([]byte(sourceID + "\x00" + status))
	return "qb-" + hex.EncodeToString(sum[:30])
}

func normalizeExternalStatus(input ExternalStatusRequest) (ExternalStatusRequest, *decimal.Decimal, error) {
	input.Type = strings.TrimSpace(input.Type)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.Status = strings.TrimSpace(input.Status)
	input.ContractRef = normalizedOptionalExternalString(input.ContractRef)
	input.LostReason = normalizedOptionalExternalString(input.LostReason)
	input.ChangedAt = input.ChangedAt.UTC().Truncate(time.Millisecond)
	if input.Type == "" {
		if strings.HasPrefix(input.Status, "报价") {
			input.Type = "报价"
		} else if strings.HasPrefix(input.Status, "投标") || input.Status == "开标中" {
			input.Type = "投标"
		}
	}
	if input.SourceID == "" || (input.Type != "报价" && input.Type != "投标") || !statusMatchesExternalType(input.Type, input.Status) || input.ChangedAt.IsZero() {
		return ExternalStatusRequest{}, nil, apperror.New(422, "CRM_OPPORTUNITY_UNKNOWN_EXTERNAL_STATUS", "unsupported external status")
	}
	if input.SourceAmount == nil {
		return input, nil, nil
	}
	raw := strings.TrimSpace(*input.SourceAmount)
	value, err := decimal.NewFromString(raw)
	if err != nil || value.IsNegative() || !value.Equal(value.Round(2)) {
		return ExternalStatusRequest{}, nil, apperror.New(422, "CRM_OPPORTUNITY_EXTERNAL_AMOUNT_INVALID", "external amount must be a non-negative decimal with at most two decimal places")
	}
	normalized := value.StringFixed(2)
	input.SourceAmount = &normalized
	return input, &value, nil
}

func normalizedOptionalExternalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func statusMatchesExternalType(externalType, status string) bool {
	if externalType == "报价" {
		return status == "报价审批中" || status == "报价已通过" || status == "报价已失效"
	}
	return status == "投标标书制作" || status == "开标中" || status == "投标开标中" || status == "投标中标" || status == "投标落标"
}

func externalLinkSnapshot(tenantID string, opportunityID uint64, input ExternalStatusRequest, amount *decimal.Decimal, createdAt time.Time) (*ExternalLink, error) {
	public := ExternalStatusSnapshot{Type: input.Type, SourceID: input.SourceID, Status: input.Status, SourceAmount: input.SourceAmount, ContractRef: input.ContractRef, LostReason: input.LostReason, ChangedAt: input.ChangedAt.UTC()}
	encoded, err := json.Marshal(public)
	if err != nil {
		return nil, err
	}
	return &ExternalLink{TenantID: tenantID, OpportunityID: opportunityID, Type: input.Type, SourceID: input.SourceID, Status: input.Status, Amount: amount, ChangedAt: input.ChangedAt.UTC(), SnapshotJSON: encoded, CreatedAt: createdAt}, nil
}

func (s *Service) persistExternalStage(ctx context.Context, principal auth.Principal, model *Opportunity, before Response, expected uint64, log StageLog, snapshot *ExternalLink) error {
	return database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if err := s.repo.UpdateStage(txCtx, model, expected); err != nil {
			return err
		}
		if err := s.repo.CreateStageLog(txCtx, &log); err != nil {
			return err
		}
		if err := s.repo.CreateExternalLink(txCtx, snapshot); err != nil {
			return err
		}
		if log.FromStage != log.ToStage || model.Status != StatusFollowing {
			if err := cancelActiveStageAlerts(txCtx, s.db, principal.TenantID, model.ID, principal.UserID, log.ChangedAt); err != nil {
				return err
			}
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "EXTERNAL_STATUS_APPLY", ResourceType: "opportunity", ResourceID: uintString(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(toResponse(model)), Result: "SUCCESS"})
	})
}

func (s *Service) ExternalStatus(ctx context.Context, id uint64) (*ExternalStatusResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	opportunityModel, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	approvedQuotation, err := s.repo.LatestApprovedQuotation(ctx, principal.TenantID, id)
	if err != nil {
		return nil, err
	}
	result := &ExternalStatusResponse{
		OpportunityID:    id,
		QuoteAmountCheck: quoteAmountCheck(opportunityModel, approvedQuotation),
	}
	if s.qbStatuses != nil {
		latest, readErr := s.qbStatuses.LatestByOpportunity(ctx, id)
		if readErr != nil {
			return nil, apperror.Wrap(readErr, 503, "INTEGRATION_QB_STATUS_UNAVAILABLE", "quotation/bid status query is unavailable")
		}
		result.Latest = latest
		return result, nil
	}
	model, err := s.repo.LatestExternalLink(ctx, principal.TenantID, id)
	if err != nil {
		return nil, err
	}
	if model == nil {
		return result, nil
	}
	var latest ExternalStatusSnapshot
	if err = json.Unmarshal(model.SnapshotJSON, &latest); err != nil || latest.Type != model.Type || latest.SourceID != model.SourceID || latest.Status != model.Status || !latest.ChangedAt.Equal(model.ChangedAt) {
		return nil, apperror.New(500, "CRM_OPPORTUNITY_EXTERNAL_STATUS_CORRUPTED", "external status snapshot is invalid")
	}
	result.Latest = &latest
	return result, nil
}

func quoteAmountCheck(model *Opportunity, approvedQuotation *ExternalLink) QuoteAmountCheck {
	result := QuoteAmountCheck{
		Status:             QuoteAmountCheckNoApprovedQuote,
		OpportunityVersion: model.Version,
		ExpectedAmount:     model.ExpectedAmount.StringFixed(2),
	}
	if approvedQuotation == nil {
		return result
	}
	sourceID := approvedQuotation.SourceID
	result.ApprovedQuoteSourceID = &sourceID
	result.ApprovedQuoteChangedAt = pointerTime(approvedQuotation.ChangedAt.UTC())
	if approvedQuotation.Amount == nil {
		result.Status = QuoteAmountCheckAmountMissing
		return result
	}
	amount := approvedQuotation.Amount.StringFixed(2)
	result.ApprovedQuoteAmount = &amount
	if model.ExpectedAmount.Equal(*approvedQuotation.Amount) {
		result.Status = QuoteAmountCheckMatch
		return result
	}
	result.Status = QuoteAmountCheckMismatch
	result.Warning = true
	return result
}

func (s *Service) CreateExternalLaunchContext(ctx context.Context, id uint64, launchType string) (*ExternalLaunchResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if s.launches == nil {
		return nil, apperror.New(503, "INTEGRATION_QB_LAUNCH_NOT_CONFIGURED", "quotation/bid launch is not configured")
	}
	result, err := s.launches.Sign(principal.TenantID, model, launchType)
	if err != nil {
		return nil, apperror.Wrap(err, 503, "INTEGRATION_QB_LAUNCH_UNAVAILABLE", "quotation/bid launch is unavailable")
	}
	if err = s.audit.Write(ctx, audit.Event{
		TenantID: principal.TenantID, Module: "opportunity", Operation: "EXTERNAL_LAUNCH_CONTEXT_CREATE",
		ResourceType: "opportunity", ResourceID: uintString(id), ActorID: principal.UserID,
		ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(struct {
			Type      string    `json:"type"`
			ExpiresAt time.Time `json:"expires_at"`
		}{Type: result.Type, ExpiresAt: result.ExpiresAt}), Result: "SUCCESS",
	}); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Service) ContractTransfer(ctx context.Context, id uint64, input ContractTransferRequest) (*ContractTransferResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return nil, ErrIdempotencyRequired
	}
	if len(key) > 128 {
		return nil, ErrIdempotencyKeyTooLong
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_CHANGE_REASON_REQUIRED", "change reason is required")
	}
	requestHash := contractTransferRequestHash(input.Version, reason)
	// 查询操作者绑定的重放坐标前先证明商机仍在数据范围内，避免原商机变化后猜测键成为 IDOR 探针。
	if _, err = s.repo.FindByID(ctx, principal, id); err != nil {
		return nil, err
	}
	var responseValue ContractTransferResponse
	runTransaction := s.contractTransferTransaction
	if runTransaction == nil {
		runTransaction = func(txCtx context.Context, fn func(context.Context) error) error {
			return database.WithTransaction(txCtx, s.db, fn)
		}
	}
	err = runTransaction(ctx, func(txCtx context.Context) error {
		prior, findErr := s.repo.FindChangeIdempotency(txCtx, principal.TenantID, id, "CONTRACT_TRANSFER", principal.UserID, key)
		if findErr != nil {
			return findErr
		}
		if prior != nil {
			if prior.RequestHash != requestHash || !json.Valid(prior.ResponseJSON) {
				return ErrIdempotencyConflict
			}
			var replay ContractTransferResponse
			if decodeErr := json.Unmarshal(prior.ResponseJSON, &replay); decodeErr != nil || replay.OpportunityID != id || replay.EventID == "" || replay.EventVersion != input.Version || replay.DeliveryStatus != "PENDING" {
				return ErrIdempotencyConflict
			}
			responseValue = replay
			return nil
		}
		model, lockErr := s.repo.FindByIDForUpdate(txCtx, principal, id)
		if lockErr != nil {
			return lockErr
		}
		// 当前事务等待商机行锁期间，相同请求可能已经提交重放记录；持锁后必须重新读取，
		// 再判断现态或创建事件，避免重复转合同。
		prior, findErr = s.repo.FindChangeIdempotencyForUpdate(txCtx, principal.TenantID, id, "CONTRACT_TRANSFER", principal.UserID, key)
		if findErr != nil {
			return findErr
		}
		if prior != nil {
			if prior.RequestHash != requestHash || !json.Valid(prior.ResponseJSON) {
				return ErrIdempotencyConflict
			}
			var replay ContractTransferResponse
			if decodeErr := json.Unmarshal(prior.ResponseJSON, &replay); decodeErr != nil || replay.OpportunityID != id || replay.EventID == "" || replay.EventVersion != input.Version || replay.DeliveryStatus != "PENDING" {
				return ErrIdempotencyConflict
			}
			responseValue = replay
			return nil
		}
		if model.Version != input.Version {
			return ErrVersionConflict
		}
		if model.Status != StatusClosed || model.CurrentStage != StageSigned || model.ContractRef == nil || strings.TrimSpace(*model.ContractRef) == "" || model.TerminalPendingType != PendingNone {
			return ErrContractTransferState
		}
		eventVersion := model.Version
		eventID := contractTransferEventID(principal.TenantID, id, eventVersion)
		responseValue = ContractTransferResponse{OpportunityID: id, EventVersion: eventVersion, EventID: eventID, DeliveryStatus: "PENDING"}
		responseJSON, encodeErr := json.Marshal(responseValue)
		if encodeErr != nil {
			return encodeErr
		}
		if existing, findErr := s.repo.FindOutboxEvent(txCtx, principal.TenantID, eventID); findErr != nil {
			return findErr
		} else if existing != nil {
			return ErrIdempotencyConflict
		}
		payload, encodeErr := json.Marshal(struct {
			OpportunityID  uint64    `json:"opportunity_id"`
			OpportunityNo  string    `json:"opportunity_no"`
			CustomerID     uint64    `json:"customer_id"`
			ContractRef    string    `json:"contract_ref"`
			ExpectedAmount string    `json:"expected_amount"`
			EventVersion   uint64    `json:"event_version"`
			OccurredAt     time.Time `json:"occurred_at"`
		}{OpportunityID: model.ID, OpportunityNo: model.OpportunityNo, CustomerID: model.CustomerID, ContractRef: strings.TrimSpace(*model.ContractRef), ExpectedAmount: model.ExpectedAmount.StringFixed(2), EventVersion: eventVersion, OccurredAt: s.now()})
		if encodeErr != nil {
			return encodeErr
		}
		if createErr := s.repo.CreateOutboxEvents(txCtx, []OutboxEvent{{EventID: eventID, TenantID: principal.TenantID, EventType: "OPPORTUNITY_SIGNED", AggregateType: "opportunity", AggregateID: uintString(id), Payload: payload, Status: "PENDING", CreatedAt: s.now()}}); createErr != nil {
			return createErr
		}
		if createErr := s.repo.CreateChangeIdempotency(txCtx, &ChangeIdempotency{TenantID: principal.TenantID, OpportunityID: id, Operation: "CONTRACT_TRANSFER", ActorID: principal.UserID, Key: key, RequestHash: requestHash, ResponseJSON: responseJSON, CreatedAt: s.now()}); createErr != nil {
			return createErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "CONTRACT_TRANSFER_ACCEPTED", ResourceType: "opportunity", ResourceID: uintString(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: responseJSON, Reason: reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	return &responseValue, nil
}

// ContractLinkCallback 原子接收合同系统的最终关联投影。幂等记录和商机更新
// 共用同一事务，合同系统重试不会重复推进商机版本。
func (s *Service) ContractLinkCallback(ctx context.Context, id uint64, input ContractLinkRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	// Normalize before hashing so harmless transport whitespace does not turn a
	// retry into a false idempotency conflict.
	input.EventID = strings.TrimSpace(input.EventID)
	input.IntakeID = strings.TrimSpace(input.IntakeID)
	input.ContractID = strings.TrimSpace(input.ContractID)
	input.ContractNumber = strings.TrimSpace(input.ContractNumber)
	input.Status = strings.TrimSpace(input.Status)
	if input.EventID == "" || input.IntakeID == "" || input.ContractNumber == "" || input.SyncVersion == 0 {
		return nil, apperror.New(422, "CRM_CONTRACT_LINK_INVALID", "contract link callback is invalid")
	}
	if input.Status != "LINK_CONFIRMED" && input.Status != "LINK_EXCEPTION" {
		return nil, apperror.New(422, "CRM_CONTRACT_LINK_INVALID", "contract link status is invalid")
	}
	if input.Status == "LINK_CONFIRMED" && !validCanonicalULID(input.ContractID) {
		return nil, apperror.New(422, "CRM_CONTRACT_LINK_INVALID", "confirmed contract id is invalid")
	}
	if input.Status == "LINK_CONFIRMED" && input.LinkedAt == nil {
		return nil, apperror.New(422, "CRM_CONTRACT_LINK_INVALID", "confirmed linked_at is required")
	}
	hashBytes := sha256.Sum256(mustJSON(input))
	requestHash := hex.EncodeToString(hashBytes[:])
	var result Response
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		// Lock the aggregate before reading the replay ledger. This matters under
		// MySQL REPEATABLE READ: two identical callbacks must not both observe an
		// empty snapshot and race into a duplicate-key error on the ledger insert.
		model, findErr := s.repo.FindByIDForUpdate(txCtx, principal, id)
		if findErr != nil {
			return findErr
		}
		prior, findErr := s.repo.FindChangeIdempotencyForUpdate(txCtx, principal.TenantID, id, "CONTRACT_LINK_CALLBACK", principal.UserID, input.EventID)
		if findErr != nil {
			return findErr
		}
		if prior != nil {
			if prior.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			if err := json.Unmarshal(prior.ResponseJSON, &result); err != nil {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if model.ContractRef == nil || strings.TrimSpace(*model.ContractRef) != strings.TrimSpace(input.ContractNumber) {
			return apperror.New(409, "CRM_CONTRACT_LINK_REFERENCE_MISMATCH", "contract number does not match opportunity contract reference")
		}
		if input.SyncVersion <= model.ContractSyncVersion {
			return apperror.New(409, "CRM_CONTRACT_LINK_STALE", "contract link callback is stale")
		}
		model.ContractIntakeID = stringPtr(strings.TrimSpace(input.IntakeID))
		model.ContractLinkStatus = input.Status
		model.ContractSyncVersion = input.SyncVersion
		model.ContractLinkEventID = stringPtr(strings.TrimSpace(input.EventID))
		if input.Status == "LINK_CONFIRMED" {
			model.ContractID = stringPtr(strings.TrimSpace(input.ContractID))
			model.ContractLinkedAt = input.LinkedAt
		} else {
			model.ContractID = nil
			model.ContractLinkedAt = nil
		}
		model.UpdatedBy = principal.UserID
		if err = s.repo.UpdateContractLink(txCtx, model, model.ContractSyncVersion-1); err != nil {
			return err
		}
		result = toResponse(model)
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return encodeErr
		}
		return s.repo.CreateChangeIdempotency(txCtx, &ChangeIdempotency{TenantID: principal.TenantID, OpportunityID: id, Operation: "CONTRACT_LINK_CALLBACK", ActorID: principal.UserID, Key: input.EventID, RequestHash: requestHash, ResponseJSON: encoded, CreatedAt: s.now()})
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func mustJSON(value any) []byte      { encoded, _ := json.Marshal(value); return encoded }
func stringPtr(value string) *string { return &value }

func contractTransferEventID(tenantID string, opportunityID, eventVersion uint64) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + uintString(opportunityID) + "\x00" + uintString(eventVersion) + "\x00OPPORTUNITY_SIGNED"))
	return "opp-signed-" + hex.EncodeToString(sum[:16])
}

func contractTransferRequestHash(version uint64, reason string) string {
	sum := sha256.Sum256([]byte(uintString(version) + "\x00" + strings.TrimSpace(reason)))
	return hex.EncodeToString(sum[:])
}

func (s *Service) persistStage(ctx context.Context, principal auth.Principal, model *Opportunity, before Response, expected uint64, log StageLog) error {
	return database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if err := s.repo.UpdateStage(txCtx, model, expected); err != nil {
			return err
		}
		if err := s.repo.CreateStageLog(txCtx, &log); err != nil {
			return err
		}
		if log.FromStage != log.ToStage || model.Status != StatusFollowing {
			if err := cancelActiveStageAlerts(txCtx, s.db, principal.TenantID, model.ID, principal.UserID, log.ChangedAt); err != nil {
				return err
			}
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity", Operation: "STAGE_CHANGE", ResourceType: "opportunity", ResourceID: uintString(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(toResponse(model)), Reason: log.Reason, Result: "SUCCESS"})
	})
}

func applyStage(model *Opportunity, stage string, contractRef, lostReason *string, pending string, changedAt time.Time, actor string) {
	model.CurrentStage, model.StageChangedAt, model.UpdatedBy = stage, changedAt, actor
	if stage == StageSigned {
		model.Status, model.ContractRef, model.LostReason, model.TerminalPendingType = StatusClosed, contractRef, nil, pending
		return
	}
	if stage == StageFailed {
		model.Status, model.ContractRef, model.LostReason, model.TerminalPendingType = StatusClosed, nil, lostReason, pending
		return
	}
	model.Status, model.ContractRef, model.LostReason, model.TerminalPendingType = StatusFollowing, nil, nil, PendingNone
}

func mapExternalStatus(status string, contractRef, lostReason *string) (string, string) {
	switch status {
	case "报价已失效":
		return StageSolution, PendingNone
	case "报价审批中", "报价已通过":
		return StageQuotation, PendingNone
	case "投标标书制作", "开标中", "投标开标中":
		return StageBid, PendingNone
	case "投标中标":
		if contractRef == nil || strings.TrimSpace(*contractRef) == "" {
			return StageSigned, PendingContract
		}
		return StageSigned, PendingNone
	case "投标落标":
		if lostReason == nil || strings.TrimSpace(*lostReason) == "" {
			return StageFailed, PendingLostReason
		}
		return StageFailed, PendingNone
	default:
		return "", ""
	}
}

func isStage(value string) bool {
	for _, item := range []string{StageInitial, StageRequirement, StageSolution, StageQuotation, StageBid, StageSigned, StageFailed} {
		if value == item {
			return true
		}
	}
	return false
}
func validLostReason(value *string) bool {
	if value == nil {
		return false
	}
	for _, item := range []string{"价格", "技术", "关系", "客户预算", "竞争对手", "其他"} {
		if *value == item {
			return true
		}
	}
	return false
}
func requirePrincipal(ctx context.Context) (auth.Principal, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok || principal.UserID == "" || principal.TenantID == "" {
		return auth.Principal{}, apperror.ErrUnauthenticated
	}
	return principal, nil
}
func pointerTime(value time.Time) *time.Time { return &value }
func uintString(value uint64) string {
	if value == 0 {
		return "0"
	}
	b := make([]byte, 0, 20)
	for value > 0 {
		b = append(b, byte('0'+value%10))
		value /= 10
	}
	for l, r := 0, len(b)-1; l < r; l, r = l+1, r-1 {
		b[l], b[r] = b[r], b[l]
	}
	return string(b)
}
