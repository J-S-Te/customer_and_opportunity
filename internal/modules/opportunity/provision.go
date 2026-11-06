package opportunity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/businessclock"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
)

// 历史合同导入开立（IMPORT_PROVISION）通道的常量。
// 该通道只服务合同系统批量导入历史合同时的“先合同后商机”开立顺序：
// 允许过去的预计签约日、允许自由文本类型/来源，但阶段状态机、赢单前置与
// 交接冻结仍按普通写入同样执行，普通创建接口的规则不变。
const (
	ProvisionOrigin = "IMPORT_PROVISION"
	// ProvisionScope 是机器凭据进入本通道所需的专用 scope；它不等价于 opportunity.create。
	ProvisionScope = "opportunity.import_provision"
	// 定稿口径：类型与来源缺省填「历史合同导入」。
	DefaultProvisionOpportunityType   = "历史合同导入"
	DefaultProvisionOpportunitySource = "历史合同导入"
	// SourceImportProvision 是阶段日志里记录来源通道的 Source 取值，
	// 它与 SourceID（合同引用摘要）共同构成阶段日志唯一键，天然阻止重复推进写两条日志。
	SourceImportProvision                       = "IMPORT_PROVISION"
	provisionIdentityMaxLength                  = 64
	provisionReasonMaxLength                    = 500
	provisionContractRefMaxLength               = 64
	provisionContractIDMaxLength                = 26
	defaultProvisionOpportunityCreateReason     = "历史合同导入开立商机"
	defaultProvisionOpportunitySignReasonPrefix = "历史合同导入推进已签约"
)

// ProvisionCreateRequest 是通道创建商机的服务层入参。
// OwnerIdentityID 来自合同侧 owner-directory 解析结果，服务端仍重新校验；
// ActorIdentityID 来自 X-Actor-Identity-ID，仅用于审计归属与业务记录操作者。
type ProvisionCreateRequest struct {
	Name               string
	CustomerID         uint64
	Type               string
	Source             string
	ExpectedAmount     string
	ExpectedSignDate   string
	RequirementSummary string
	SystemCount        uint32
	PainPoints         string
	CompetitorInfo     string
	OwnerIdentityID    string
	ActorIdentityID    string
	ExternalRecordID   string
	IdempotencyKey     string
	Reason             string
}

// ProvisionSignRequest 是通道推进「已签约 + CLOSED」的服务层入参。
// ContractID 只进入审计（不写商机的合同关联列，避免提前把商机标记为已回写合同）。
type ProvisionSignRequest struct {
	ContractRef      string
	ContractID       string
	ExpectedVersion  uint64
	Reason           string
	ActorIdentityID  string
	ExternalRecordID string
}

var (
	// ErrProvisionInvalidRequest 表示通道入参无效（编号、合同引用、版本、原因等）。
	ErrProvisionInvalidRequest = apperror.New(http.StatusUnprocessableEntity, "CRM_IMPORT_PROVISION_INVALID_REQUEST", "历史合同导入开立请求无效")
	// ErrProvisionOwnerInvalid 表示 owner_identity_id 缺失、格式非法、为机器身份或未通过目录校验。
	ErrProvisionOwnerInvalid = apperror.New(http.StatusUnprocessableEntity, "CRM_IMPORT_PROVISION_OWNER_INVALID", "owner_identity_id 缺失或未通过平台负责人目录校验")
	// ErrProvisionOwnerDirectoryUnavailable 表示目录不可用，必须拒绝而不是跳过校验。
	ErrProvisionOwnerDirectoryUnavailable = apperror.New(http.StatusServiceUnavailable, "CRM_IMPORT_PROVISION_OWNER_DIRECTORY_UNAVAILABLE", "平台负责人目录不可用，无法校验 owner_identity_id，请稍后重试")
	// ErrProvisionContractNotVerified 表示合同验真依赖未配置或不可用。
	// 历史合同必须带可验真的 contract_ref 才能推进到已签约，本通道不接受“仅本地证据”推进，
	// 因此这里给出可操作提示：配置合同验真凭据，而不是更换推进方式绕过。
	ErrProvisionContractNotVerified = apperror.New(http.StatusServiceUnavailable, "CRM_IMPORT_PROVISION_CONTRACT_NOT_VERIFIED",
		"合同验真未配置或不可用：请为 CRM 配置合同验真凭据（合同引用服务地址与凭据）后重试；历史合同导入通道必须凭可验真的 contract_ref 推进，不能改用本地证据")
	// ErrProvisionContractCustomerMismatch 复用交互式阶段变更的既有口径：
	// 合同不属于该商机客户时拒绝推进，避免跨客户挂合同。
	ErrProvisionContractCustomerMismatch = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_CONTRACT_CUSTOMER_MISMATCH", "contract belongs to another customer")
)

// CreateProvision 按历史导入开立通道创建商机。
// 参数：ctx 携带机器身份，request 为通道入参。
// 返回值：创建或幂等重放的商机；负责人、客户可见性、金额、日期或幂等校验失败时拒绝。
func (s *Service) CreateProvision(ctx context.Context, request ProvisionCreateRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.HasPermission(ProvisionScope) {
		return nil, apperror.ErrForbidden
	}
	request = normalizeProvisionCreateRequest(request)
	if err = validateProvisionCreateRequest(request); err != nil {
		return nil, err
	}
	// 负责人必须显式给出并通过目录校验；绝不回退到机器身份作为负责人。
	ownerOrgID, err := s.validateProvisionOwner(ctx, request.OwnerIdentityID)
	if err != nil {
		return nil, err
	}
	if err = s.ensureOwnerNotFrozen(ctx, principal.TenantID, request.OwnerIdentityID); err != nil {
		return nil, err
	}
	if err = validateOpportunityMasterDataSelection(request.Name, request.Type, request.Source, request.RequirementSummary, nil, nil); err != nil {
		return nil, err
	}
	amount, err := decimal.NewFromString(request.ExpectedAmount)
	if err != nil || !amount.IsPositive() {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_INVALID_AMOUNT", "expected amount must be greater than zero")
	}
	signDate, err := time.Parse("2006-01-02", request.ExpectedSignDate)
	if err != nil {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_INVALID_SIGN_DATE", "expected sign date must be a valid calendar date")
	}
	// 与普通创建的唯一差异：历史合同的合同开始日可能在过去，本通道不校验「不早于今天」。
	// 普通创建仍在 service.go 的 Create 里保持该前置。
	key := request.IdempotencyKey
	input := CreateRequest{
		Name: request.Name, CustomerID: request.CustomerID, Type: request.Type, Source: request.Source,
		ExpectedAmount: request.ExpectedAmount, ExpectedSignDate: request.ExpectedSignDate,
		RequirementSummary: request.RequirementSummary, SystemCount: request.SystemCount,
		PainPoints: request.PainPoints, CompetitorInfo: request.CompetitorInfo,
		OwnerUserID: request.OwnerIdentityID, OwnerOrgID: ownerOrgID, IdempotencyKey: key,
	}
	requestHash, err := provisionCreateRequestHash(input, request.ActorIdentityID, request.ExternalRecordID)
	if err != nil {
		return nil, err
	}
	if prior, findErr := s.repo.FindCreateIdempotency(ctx, principal.TenantID, principal.UserID, key); findErr != nil {
		return nil, findErr
	} else if prior != nil {
		return s.replayProvisionCreate(ctx, principal, request, ownerOrgID, requestHash, prior)
	}
	model := &Opportunity{
		Name: request.Name, CustomerID: request.CustomerID, Type: request.Type, Source: request.Source,
		ExpectedAmount: amount, ExpectedSignDate: signDate, RequirementSummary: request.RequirementSummary,
		SystemCount: request.SystemCount, PainPoints: request.PainPoints, CompetitorInfo: request.CompetitorInfo,
		OwnerUserID: request.OwnerIdentityID, OwnerOrgID: ownerOrgID,
		CurrentStage: StageInitial, Status: StatusFollowing, ContractLinkStatus: "PENDING", TerminalPendingType: PendingNone, StageChangedAt: s.now(),
	}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, request.ActorIdentityID, request.ActorIdentityID, 1
	var response Response
	err = s.runCreateTransaction(ctx, func(txCtx context.Context) error {
		// 先证明客户授权范围，再查重放记录，避免调用方通过猜测幂等键探测范围外客户。
		visible, visibleErr := s.repo.CustomerVisible(txCtx, principal, request.CustomerID)
		if visibleErr != nil {
			return visibleErr
		}
		if !visible {
			return ErrCustomerForbidden
		}
		prior, findErr := s.repo.FindCreateIdempotency(txCtx, principal.TenantID, principal.UserID, key)
		if findErr != nil {
			return findErr
		}
		if prior != nil {
			replayed, replayErr := s.replayProvisionCreate(txCtx, principal, request, ownerOrgID, requestHash, prior)
			if replayErr != nil {
				return replayErr
			}
			response = *replayed
			return nil
		}
		// 事务内最终防线：交接冻结可能在事务开启后才落库。
		if guardErr := s.guardOwnerWrite(txCtx, principal.TenantID, request.OwnerIdentityID); guardErr != nil {
			return guardErr
		}
		// 编号日期段属于业务日历值，必须按北京时间取日；存储时间戳仍保持 UTC。
		number, numberErr := s.repo.NextNumber(txCtx, principal.TenantID, businessclock.DateString(s.now()))
		if numberErr != nil {
			return numberErr
		}
		model.OpportunityNo = number
		if createErr := s.repo.Create(txCtx, model); createErr != nil {
			return createErr
		}
		created := toResponse(model)
		encoded, encodeErr := json.Marshal(created)
		if encodeErr != nil {
			return encodeErr
		}
		if idemErr := s.repo.CreateCreateIdempotency(txCtx, &CreateIdempotency{
			TenantID: principal.TenantID, ActorID: principal.UserID, Key: key,
			CustomerID: request.CustomerID, OpportunityID: model.ID,
			RequestHash: requestHash, ResponseHash: bytesHash(encoded), ResponseJSON: encoded,
			RequestIDTrace: requestctx.ID(ctx), CreatedAt: s.now(),
		}); idemErr != nil {
			return idemErr
		}
		if auditErr := s.audit.Write(txCtx, audit.Event{
			TenantID: principal.TenantID, Module: "opportunity", Operation: "IMPORT_PROVISION_CREATE", ResourceType: "opportunity",
			ResourceID: uintString(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName,
			AfterJSON: audit.JSON(provisionOpportunitySnapshot{Origin: ProvisionOrigin, ExternalRecordID: request.ExternalRecordID,
				ActorIdentityID: request.ActorIdentityID, Opportunity: created}), Reason: request.Reason, Result: "SUCCESS",
		}); auditErr != nil {
			return auditErr
		}
		response = created
		return nil
	})
	if err != nil {
		if isDuplicateCreateError(err) {
			prior, findErr := s.repo.FindCreateIdempotency(ctx, principal.TenantID, principal.UserID, key)
			if findErr == nil && prior != nil {
				return s.replayProvisionCreate(ctx, principal, request, ownerOrgID, requestHash, prior)
			}
		}
		return nil, err
	}
	return &response, nil
}

// SignProvision 把历史导入创建的商机推进到「已签约 + CLOSED」。
// 参数：ctx 携带机器身份，id 为商机标识，request 为合同引用、期望版本与原因。
// 返回值：推进后的商机、是否命中幂等回放，以及错误；同合同引用重复调用返回既有结果，不重复写阶段日志。
func (s *Service) SignProvision(ctx context.Context, id uint64, request ProvisionSignRequest) (*Response, bool, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, false, err
	}
	if !principal.HasPermission(ProvisionScope) {
		return nil, false, apperror.ErrForbidden
	}
	request = normalizeProvisionSignRequest(request)
	if id == 0 {
		return nil, false, ErrProvisionInvalidRequest
	}
	if err = validateProvisionSignRequest(request); err != nil {
		return nil, false, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, false, err
	}
	// 幂等：同一 contract_ref 已经推进到终态时直接回放既有结果，不再写阶段日志。
	// 这正是设计要求的"同 contract_ref 重复调用返回既有结果"。
	if model.CurrentStage == StageSigned && model.Status == StatusClosed && model.ContractRef != nil &&
		strings.TrimSpace(*model.ContractRef) == request.ContractRef {
		result := toResponse(model)
		return &result, true, nil
	}
	if model.Version != request.ExpectedVersion {
		return nil, false, ErrVersionConflict
	}
	// 必须走既有阶段状态机：终态、作废与非法迁移都由同一闸门拒绝。
	if err = validateStageChange(model, StageSigned); err != nil {
		return nil, false, err
	}
	// 合同验真闸门复用既有口径（与 ChangeStage 的 StageSigned 分支一致）：
	// 目标已签约必须凭可验真的 contract_ref，未配置依赖一律 503，绝不放过。
	if s.contracts == nil {
		return nil, false, ErrProvisionContractNotVerified
	}
	belongs, verifyErr := s.contracts.BelongsToCustomer(ctx, request.ContractRef, model.CustomerID)
	if verifyErr != nil {
		return nil, false, apperror.Wrap(verifyErr, http.StatusServiceUnavailable, "CRM_IMPORT_PROVISION_CONTRACT_NOT_VERIFIED", ErrProvisionContractNotVerified.Message)
	}
	if !belongs {
		return nil, false, ErrProvisionContractCustomerMismatch
	}
	contractRef := request.ContractRef
	before := toResponse(model)
	// 业务操作者取 X-Actor-Identity-ID：阶段日志的 operator_id 与商机 updated_by 都记它，
	// 机器凭据本身只出现在审计事件（按 machine: 前缀标记 MACHINE）。
	operator := request.ActorIdentityID
	applyStage(model, StageSigned, &contractRef, nil, PendingNone, s.now(), operator)
	log := StageLog{
		TenantID: principal.TenantID, OpportunityID: model.ID,
		FromStage: before.CurrentStage, ToStage: model.CurrentStage,
		Source: SourceImportProvision, SourceID: provisionStageSourceID(request.ContractRef),
		Reason: provisionSignReason(request.Reason), ContractRef: model.ContractRef,
		PendingType: model.TerminalPendingType, OperatorID: operator, ChangedAt: model.StageChangedAt, RequestID: requestctx.ID(ctx),
	}
	// evidence 恒为 nil：本通道不产出赢单/合同确认证据，只写阶段与阶段日志。
	if err = s.persistStage(ctx, principal, model, before, request.ExpectedVersion, log, nil); err != nil {
		// 并发重复推进同一合同引用会撞阶段日志唯一键；恢复既有结果而不是报 500。
		if isDuplicateStageSource(err) {
			if current, findErr := s.repo.FindByID(ctx, principal, id); findErr == nil && current.CurrentStage == StageSigned && current.Status == StatusClosed &&
				current.ContractRef != nil && strings.TrimSpace(*current.ContractRef) == request.ContractRef {
				result := toResponse(current)
				return &result, true, nil
			}
			return nil, false, ErrIdempotencyConflict
		}
		return nil, false, err
	}
	result := toResponse(model)
	snapshot := provisionOpportunitySnapshot{Origin: ProvisionOrigin, ExternalRecordID: request.ExternalRecordID,
		ActorIdentityID: request.ActorIdentityID, Opportunity: result}
	// 来源审计：阶段日志与 STAGE_CHANGE 审计已在同一事务内落库，这里补充通道来源与合同标识。
	if err = s.audit.Write(ctx, audit.Event{
		TenantID: principal.TenantID, Module: "opportunity", Operation: "IMPORT_PROVISION_SIGN", ResourceType: "opportunity",
		ResourceID: uintString(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName,
		AfterJSON: audit.JSON(provisionSignSnapshot{provisionOpportunitySnapshot: snapshot, ContractID: request.ContractID,
			ContractRef: request.ContractRef}), Reason: provisionSignReason(request.Reason), Result: "SUCCESS",
	}); err != nil {
		return nil, false, err
	}
	return &result, false, nil
}

func normalizeProvisionCreateRequest(input ProvisionCreateRequest) ProvisionCreateRequest {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.TrimSpace(input.Type)
	input.Source = strings.TrimSpace(input.Source)
	input.ExpectedAmount = strings.TrimSpace(input.ExpectedAmount)
	input.ExpectedSignDate = strings.TrimSpace(input.ExpectedSignDate)
	input.RequirementSummary = strings.TrimSpace(input.RequirementSummary)
	input.PainPoints = strings.TrimSpace(input.PainPoints)
	input.CompetitorInfo = strings.TrimSpace(input.CompetitorInfo)
	input.OwnerIdentityID = strings.TrimSpace(input.OwnerIdentityID)
	input.ActorIdentityID = strings.TrimSpace(input.ActorIdentityID)
	input.ExternalRecordID = strings.TrimSpace(input.ExternalRecordID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Reason = strings.TrimSpace(input.Reason)
	// 定稿口径：类型与来源缺省填「历史合同导入」。
	if input.Type == "" {
		input.Type = DefaultProvisionOpportunityType
	}
	if input.Source == "" {
		input.Source = DefaultProvisionOpportunitySource
	}
	if input.Reason == "" {
		input.Reason = defaultProvisionOpportunityCreateReason
	}
	return input
}

func validateProvisionCreateRequest(request ProvisionCreateRequest) error {
	if request.IdempotencyKey == "" {
		return ErrIdempotencyRequired
	}
	if len(request.IdempotencyKey) > 128 {
		return ErrIdempotencyKeyTooLong
	}
	if request.CustomerID == 0 || !validProvisionIdentity(request.ActorIdentityID) {
		return ErrProvisionInvalidRequest
	}
	if utf8.RuneCountInString(request.Reason) > provisionReasonMaxLength {
		return ErrProvisionInvalidRequest
	}
	if request.ExternalRecordID != "" && (len(request.ExternalRecordID) > 128 || unsafeProvisionText(request.ExternalRecordID)) {
		return ErrProvisionInvalidRequest
	}
	return nil
}

func normalizeProvisionSignRequest(input ProvisionSignRequest) ProvisionSignRequest {
	input.ContractRef = strings.TrimSpace(input.ContractRef)
	input.ContractID = strings.TrimSpace(input.ContractID)
	input.Reason = strings.TrimSpace(input.Reason)
	input.ActorIdentityID = strings.TrimSpace(input.ActorIdentityID)
	input.ExternalRecordID = strings.TrimSpace(input.ExternalRecordID)
	input.Reason = provisionSignReason(input.Reason)
	return input
}

func validateProvisionSignRequest(request ProvisionSignRequest) error {
	if request.ExpectedVersion == 0 || !validProvisionIdentity(request.ActorIdentityID) {
		return ErrProvisionInvalidRequest
	}
	if request.ContractRef == "" || len(request.ContractRef) > provisionContractRefMaxLength || unsafeProvisionText(request.ContractRef) {
		return ErrProvisionInvalidRequest
	}
	if request.ContractID != "" && (len(request.ContractID) > provisionContractIDMaxLength || unsafeProvisionText(request.ContractID)) {
		return ErrProvisionInvalidRequest
	}
	if request.ExternalRecordID != "" && (len(request.ExternalRecordID) > 128 || unsafeProvisionText(request.ExternalRecordID)) {
		return ErrProvisionInvalidRequest
	}
	if utf8.RuneCountInString(request.Reason) > provisionReasonMaxLength {
		return ErrProvisionInvalidRequest
	}
	return nil
}

// provisionSignReason 给阶段日志补上通道语义，同时保留合同侧给出的业务原因。
func provisionSignReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return defaultProvisionOpportunitySignReasonPrefix
	}
	return reason
}

// validateProvisionOwner 用平台负责人目录校验 (owner, 主组织) 组合，并返回该组织。
// fail-closed：目录未接线、不可用或校验失败都拒绝，绝不跳过校验。
func (s *Service) validateProvisionOwner(ctx context.Context, ownerIdentityID string) (string, error) {
	if !validProvisionIdentity(ownerIdentityID) {
		return "", ErrProvisionOwnerInvalid
	}
	if strings.HasPrefix(strings.ToLower(ownerIdentityID), "machine:") {
		return "", ErrProvisionOwnerInvalid
	}
	if s.owners == nil {
		return "", ErrProvisionOwnerDirectoryUnavailable
	}
	page, err := s.owners.List(ctx, ownerdirectory.Query{UserID: ownerIdentityID, Page: 1, PageSize: 1})
	if err != nil {
		if errors.Is(err, ownerdirectory.ErrUnavailable) {
			return "", ErrProvisionOwnerDirectoryUnavailable
		}
		return "", err
	}
	organizationID := ownerdirectory.PrimaryOrganization(page, ownerIdentityID)
	if err = s.owners.Validate(ctx, ownerIdentityID, organizationID); err != nil {
		if errors.Is(err, ownerdirectory.ErrUnavailable) {
			return "", ErrProvisionOwnerDirectoryUnavailable
		}
		return "", apperror.WithDetails(ErrProvisionOwnerInvalid, struct {
			OwnerIdentityID string `json:"owner_identity_id"`
			OrganizationID  string `json:"organization_id,omitempty"`
		}{OwnerIdentityID: ownerIdentityID, OrganizationID: organizationID})
	}
	return organizationID, nil
}

// validProvisionIdentity 校验平台身份标识的字面合法性（长度、控制字符、空白）。
func validProvisionIdentity(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > provisionIdentityMaxLength || unsafeProvisionText(value) {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n")
}

// unsafeProvisionText 拒绝 HTML 尖括号与控制字符，保持与客户模块输入校验同口径。
func unsafeProvisionText(value string) bool {
	if strings.ContainsAny(value, "<>") {
		return true
	}
	for _, char := range value {
		if unicode.IsControl(char) && char != '\n' && char != '\r' && char != '\t' {
			return true
		}
	}
	return false
}

// provisionCreateRequestHash 在普通创建摘要之外绑定来源通道、操作者身份与外部记录标识。
func provisionCreateRequestHash(input CreateRequest, actorIdentityID, externalRecordID string) (string, error) {
	base, err := createRequestHash(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(base + "\x00" + ProvisionOrigin + "\x00" + actorIdentityID + "\x00" + externalRecordID))
	return hex.EncodeToString(digest[:]), nil
}

// replayProvisionCreate 校验幂等坐标并回放既有结果。
// 与交互式创建不同：业务记录的 created_by 是操作者平台身份（X-Actor-Identity-ID），
// 幂等表的 actor_id 是机器凭据主体，两者都要在重放时精确匹配。
func (s *Service) replayProvisionCreate(ctx context.Context, principal auth.Principal, request ProvisionCreateRequest, ownerOrgID, requestHash string, prior *CreateIdempotency) (*Response, error) {
	if err := validateCreateReplay(prior, principal, request.CustomerID, request.IdempotencyKey, requestHash); err != nil {
		return nil, err
	}
	model, err := s.repo.FindCreatedByID(ctx, principal.TenantID, prior.OpportunityID)
	if err != nil {
		return nil, err
	}
	if model.CustomerID != request.CustomerID || model.CreatedBy != request.ActorIdentityID || model.OwnerUserID != request.OwnerIdentityID || model.OwnerOrgID != ownerOrgID {
		return nil, ErrIdempotencyConflict
	}
	if bytesHash(prior.ResponseJSON) != prior.ResponseHash {
		return nil, ErrIdempotencyConflict
	}
	var result Response
	if err = json.Unmarshal(prior.ResponseJSON, &result); err != nil {
		return nil, ErrIdempotencyConflict
	}
	if result.ID != prior.OpportunityID || result.CustomerID != request.CustomerID {
		return nil, ErrIdempotencyConflict
	}
	return &result, nil
}

// provisionStageSourceID 由合同引用派生稳定的阶段日志来源标识，
// 与 (tenant, source, source_id) 唯一键一起保证同一合同只写一条推进日志。
func provisionStageSourceID(contractRef string) string {
	digest := sha256.Sum256([]byte(ProvisionOrigin + "\x00" + contractRef))
	return "prov-" + hex.EncodeToString(digest[:20])
}

// isDuplicateStageSource 判断并发重复推进是否撞到阶段日志来源唯一键。
func isDuplicateStageSource(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return strings.Contains(strings.ToLower(mysqlErr.Message), "uk_stage_source")
	}
	return errors.Is(err, gorm.ErrDuplicatedKey)
}

// provisionOpportunitySnapshot 是通道创建/推进审计的可序列化快照。
type provisionOpportunitySnapshot struct {
	Origin           string   `json:"origin"`
	ExternalRecordID string   `json:"external_record_id,omitempty"`
	ActorIdentityID  string   `json:"actor_identity_id"`
	Opportunity      Response `json:"opportunity"`
}

// provisionSignSnapshot 在创建快照之上补充合同标识，便于事后追溯验真依据。
type provisionSignSnapshot struct {
	provisionOpportunitySnapshot
	ContractRef string `json:"contract_ref"`
	ContractID  string `json:"contract_id,omitempty"`
}
