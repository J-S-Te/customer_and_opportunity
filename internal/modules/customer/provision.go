package customer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/businessclock"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
)

// 历史合同导入开立（IMPORT_PROVISION）通道的常量。
// 该通道只服务合同系统批量导入历史合同时的“先合同后商机”开立顺序，
// 普通创建接口（POST /api/v1/customers）的校验强度、默认值与权限保持不变。
const (
	ProvisionOrigin = "IMPORT_PROVISION"
	// ProvisionScope 是机器凭据进入本通道所需的专用 scope；
	// 它不等价于 customer.create，普通创建链路仍只认 customer.create。
	ProvisionScope = "customer.import_provision"
	// 定稿口径：客户必填三项允许为空，空时由服务端补齐以下默认值。
	DefaultProvisionCustomerType = "企业"
	DefaultProvisionIndustry     = "其他"
	DefaultProvisionRegion       = "未填写"
	// 平台身份标识（负责人与操作者）与库列长度一致，避免截断后再校验。
	provisionIdentityMaxLength = 64
	provisionReasonMaxLength   = 500
	defaultProvisionReason     = "历史合同导入开立客户"
)

// ProvisionCandidate 是名称精确匹配命中的客户摘要，供合同侧判定是否阻断该行。
type ProvisionCandidate struct {
	ID      uint64 `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Enabled bool   `json:"enabled"`
}

// ProvisionLookup 是名称精确查询结果。
// Unique 仅在“恰好一个候选且该候选仍可用”时为 true；停用或已合并客户也算命中，
// 由合同侧按定稿口径阻断该行，不在此处静默放行。
type ProvisionLookup struct {
	Candidates     []ProvisionCandidate `json:"candidates"`
	Unique         bool                 `json:"unique"`
	NormalizedName string               `json:"normalized_name"`
}

// ProvisionDirectory 是历史导入开立通道专用的仓储扩展点。
// 它按租户在全量客户内判定名称唯一性（不做数据范围收窄），
// 与交互式查重接口的可见性口径不同，因此单列为可选扩展点。
type ProvisionDirectory interface {
	FindProvisionCandidates(ctx context.Context, tenantID, normalizedName string) ([]ProvisionCandidate, error)
}

// ProvisionCreateRequest 是通道创建客户的服务层入参。
// OwnerIdentityID 来自合同侧的 owner-directory 解析结果，服务端仍会重新校验；
// ActorIdentityID 来自 X-Actor-Identity-ID，只用于审计归属与业务记录的操作者，不作为负责人。
type ProvisionCreateRequest struct {
	Name             string
	CustomerType     string
	Industry         string
	Region           string
	OwnerIdentityID  string
	Contacts         []ContactInput
	ActorIdentityID  string
	ExternalRecordID string
	IdempotencyKey   string
	Reason           string
}

var (
	// ErrProvisionUnavailable 表示通道仓储未接线，不能判定名称唯一性。
	ErrProvisionUnavailable = apperror.New(http.StatusServiceUnavailable, "CRM_IMPORT_PROVISION_UNAVAILABLE", "历史合同导入开立通道未接线，请配置客户仓储后重试")
	// ErrProvisionInvalidRequest 表示请求字段无效（名称、键、原因等）。
	ErrProvisionInvalidRequest = apperror.New(http.StatusUnprocessableEntity, "CRM_IMPORT_PROVISION_INVALID_REQUEST", "历史合同导入开立请求无效")
	// ErrProvisionOwnerInvalid 表示 owner_identity_id 缺失、格式非法、为机器身份或未通过目录校验。
	ErrProvisionOwnerInvalid = apperror.New(http.StatusUnprocessableEntity, "CRM_IMPORT_PROVISION_OWNER_INVALID", "owner_identity_id 缺失或未通过平台负责人目录校验")
	// ErrProvisionOwnerDirectoryUnavailable 表示目录不可用，必须拒绝而不是跳过校验。
	ErrProvisionOwnerDirectoryUnavailable = apperror.New(http.StatusServiceUnavailable, "CRM_IMPORT_PROVISION_OWNER_DIRECTORY_UNAVAILABLE", "平台负责人目录不可用，无法校验 owner_identity_id，请稍后重试")
	// ErrProvisionCustomerAmbiguous 表示名称已命中既有客户（单个或多个），通道不允许重复建档。
	ErrProvisionCustomerAmbiguous = apperror.New(http.StatusConflict, "CRM_IMPORT_PROVISION_CUSTOMER_AMBIGUOUS", "客户名称已命中既有客户，请按候选处理后重试")
)

// LookupProvisionCandidates 按客户名称归一化后精确查询候选客户。
// 参数：ctx 携带机器身份，name 为客户名称原文。
// 返回值：候选列表、唯一命中标记与归一化名称；名称非法或缺 scope 时拒绝。
func (s *Service) LookupProvisionCandidates(ctx context.Context, name string) (ProvisionLookup, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return ProvisionLookup{}, err
	}
	if !principal.HasPermission(ProvisionScope) {
		return ProvisionLookup{}, apperror.ErrForbidden
	}
	directory, ok := s.provisionDirectory()
	if !ok {
		return ProvisionLookup{}, ErrProvisionUnavailable
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > 200 || unsafeText(trimmed) {
		return ProvisionLookup{}, ErrProvisionInvalidRequest
	}
	// 归一化规则与交互式查重、客户建档完全一致（trim + 去空白 + 小写），
	// 避免同一客户在两条链路上得到不同的名称等价判断。
	normalized := normalizeName(trimmed)
	candidates, err := directory.FindProvisionCandidates(ctx, principal.TenantID, normalized)
	if err != nil {
		return ProvisionLookup{}, err
	}
	if candidates == nil {
		candidates = make([]ProvisionCandidate, 0)
	}
	result := ProvisionLookup{Candidates: candidates, Unique: len(candidates) == 1 && candidates[0].Enabled, NormalizedName: normalized}
	// 查询本身也要留痕：合同系统据此决定是否阻断整行，事后必须能还原判定依据。
	if err = s.audit.Write(ctx, audit.Event{
		TenantID: principal.TenantID, Module: "customer", Operation: "IMPORT_PROVISION_LOOKUP", ResourceType: "customer",
		ResourceID: provisionLookupResourceID(normalized), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName,
		AfterJSON: audit.JSON(provisionLookupSnapshot{Origin: ProvisionOrigin, NormalizedName: normalized, Unique: result.Unique, Candidates: candidates}),
		Result:    "SUCCESS",
	}); err != nil {
		return ProvisionLookup{}, err
	}
	return result, nil
}

// CreateProvision 按历史导入开立通道创建客户。
// 参数：ctx 携带机器身份，request 为通道入参（负责人显式给出并由目录复核）。
// 返回值：创建或幂等重放的客户；负责人、查重、幂等或冻结校验失败时拒绝。
func (s *Service) CreateProvision(ctx context.Context, request ProvisionCreateRequest) (*Response, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.HasPermission(ProvisionScope) {
		return nil, apperror.ErrForbidden
	}
	if s.create == nil {
		return nil, ErrCreateIdempotencyUnavailable
	}
	request = normalizeProvisionCreateRequest(request)
	if err = validateProvisionCreateRequest(request); err != nil {
		return nil, err
	}
	// 负责人必须显式给出并通过目录校验；本通道绝不回退到认证主体（机器身份）作为负责人。
	ownerOrgID, err := s.validateProvisionOwner(ctx, request.OwnerIdentityID)
	if err != nil {
		return nil, err
	}
	if err = s.ensureOwnerNotFrozen(ctx, principal.TenantID, request.OwnerIdentityID); err != nil {
		return nil, err
	}
	if err = validateRegistrationContacts(request.Contacts); err != nil {
		return nil, err
	}
	requestHash, err := s.provisionCreateRequestHash(principal, request, ownerOrgID)
	if err != nil {
		return nil, err
	}
	if prior, findErr := s.create.FindCreateIdempotency(ctx, principal.TenantID, principal.UserID, request.IdempotencyKey); findErr != nil {
		return nil, findErr
	} else if prior != nil {
		return s.replayProvisionCreate(ctx, principal, request, requestHash, prior)
	}
	// 通道内同名即冲突：合同侧应先 lookup 再决定阻断，不允许“猜着重建”。
	if err = s.rejectProvisionDuplicateNames(ctx, principal, request.Name); err != nil {
		return nil, err
	}
	model, err := s.buildProvisionCustomer(principal, request, ownerOrgID)
	if err != nil {
		return nil, err
	}
	var response *Response
	err = s.create.WithCreateTransaction(ctx, func(txCtx context.Context) error {
		// 事务内重新读取幂等记录与同名客户，关闭“事务外预检后并发插入”的时间窗口。
		prior, findErr := s.create.FindCreateIdempotency(txCtx, principal.TenantID, principal.UserID, request.IdempotencyKey)
		if findErr != nil {
			return findErr
		}
		if prior != nil {
			var replayErr error
			response, replayErr = s.replayProvisionCreate(txCtx, principal, request, requestHash, prior)
			return replayErr
		}
		if duplicateErr := s.rejectProvisionDuplicateNames(txCtx, principal, request.Name); duplicateErr != nil {
			return duplicateErr
		}
		// 事务内最终防线：交接冻结可能在事务开启后才落库。
		if guardErr := s.guardOwnerWrite(txCtx, principal.TenantID, request.OwnerIdentityID); guardErr != nil {
			return guardErr
		}
		if persistErr := s.persistProvisionCustomer(txCtx, principal, model, request); persistErr != nil {
			return persistErr
		}
		value := toResponse(model)
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			return encodeErr
		}
		if replayErr := s.create.CreateCreateIdempotency(txCtx, &CreateIdempotency{
			TenantID: principal.TenantID, ActorID: principal.UserID, Key: request.IdempotencyKey,
			RequestHash: requestHash, CustomerID: model.ID, Status: "COMPLETED", ResponseJSON: encoded,
			ResponseHash: responseDigest(encoded), CreatedAt: s.now().UTC(),
		}); replayErr != nil {
			return replayErr
		}
		response = &value
		return nil
	})
	if err != nil {
		if isCustomerCreateRaceCandidate(err) {
			prior, findErr := s.create.FindCreateIdempotency(ctx, principal.TenantID, principal.UserID, request.IdempotencyKey)
			if findErr == nil && prior != nil {
				return s.replayProvisionCreate(ctx, principal, request, requestHash, prior)
			}
		}
		return nil, err
	}
	return response, nil
}

// provisionDirectory 返回通道仓储扩展点；未接线时调用方按 503 拒绝。
func (s *Service) provisionDirectory() (ProvisionDirectory, bool) {
	if s == nil || s.repo == nil {
		return nil, false
	}
	directory, ok := s.repo.(ProvisionDirectory)
	return directory, ok
}

func normalizeProvisionCreateRequest(input ProvisionCreateRequest) ProvisionCreateRequest {
	input.Name = strings.TrimSpace(input.Name)
	input.CustomerType = strings.TrimSpace(input.CustomerType)
	input.Industry = strings.TrimSpace(input.Industry)
	input.Region = strings.TrimSpace(input.Region)
	input.OwnerIdentityID = strings.TrimSpace(input.OwnerIdentityID)
	input.ActorIdentityID = strings.TrimSpace(input.ActorIdentityID)
	input.ExternalRecordID = strings.TrimSpace(input.ExternalRecordID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Reason = strings.TrimSpace(input.Reason)
	// 定稿口径：三项主数据为空时填默认值；非空值原样保留，不覆盖合同侧提供的历史值。
	if input.CustomerType == "" {
		input.CustomerType = DefaultProvisionCustomerType
	}
	if input.Industry == "" {
		input.Industry = DefaultProvisionIndustry
	}
	if input.Region == "" {
		input.Region = DefaultProvisionRegion
	}
	if input.Reason == "" {
		input.Reason = defaultProvisionReason
	}
	for index := range input.Contacts {
		input.Contacts[index].Name = strings.TrimSpace(input.Contacts[index].Name)
		input.Contacts[index].Phone = strings.TrimSpace(input.Contacts[index].Phone)
		input.Contacts[index].Email = strings.TrimSpace(input.Contacts[index].Email)
	}
	return input
}

func validateProvisionCreateRequest(request ProvisionCreateRequest) error {
	// 幂等键复用既有错误码，保持四个写接口一致的客户端处理方式。
	if request.IdempotencyKey == "" {
		return ErrIdempotencyRequired
	}
	if len(request.IdempotencyKey) > 128 {
		return ErrIdempotencyInvalid
	}
	if utf8.RuneCountInString(request.Reason) > provisionReasonMaxLength {
		return ErrProvisionInvalidRequest
	}
	if request.ExternalRecordID != "" && (len(request.ExternalRecordID) > 128 || unsafeText(request.ExternalRecordID)) {
		return ErrProvisionInvalidRequest
	}
	if !validProvisionIdentity(request.ActorIdentityID) {
		return ErrProvisionInvalidRequest
	}
	return validateCustomerMasterData(request.Name, request.CustomerType, request.Industry, request.Region, request.Reason)
}

// validateProvisionOwner 用平台负责人目录校验 (owner, 主组织) 组合，并返回该组织。
// 本函数是 fail-closed 的：目录未接线、不可用或校验失败都返回错误，绝不跳过校验。
func (s *Service) validateProvisionOwner(ctx context.Context, ownerIdentityID string) (string, error) {
	if !validProvisionIdentity(ownerIdentityID) {
		return "", ErrProvisionOwnerInvalid
	}
	// 机器身份不是平台人员，不能成为客户负责人；先拒绝再进入目录调用，避免无谓依赖调用。
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
	if value == "" || len(value) > provisionIdentityMaxLength || unsafeText(value) {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n")
}

// rejectProvisionDuplicateNames 在通道内把“名称已占用”判为歧义冲突。
// 定稿口径是归一化后“精确匹配”，因此优先使用通道仓储的精确查询；
// 交互式查重是 LIKE 语义，只会把 A 与「A公司」判成同一客户，不能用于本通道。
// 与交互式创建不同，这里不允许 duplicate_override：历史导入必须由合同侧
// 先按 lookup 结果定位既有客户，不能靠覆盖选项重复建档。
func (s *Service) rejectProvisionDuplicateNames(ctx context.Context, principal auth.Principal, name string) error {
	normalized := normalizeName(name)
	var candidates []ProvisionCandidate
	if directory, ok := s.provisionDirectory(); ok {
		found, err := directory.FindProvisionCandidates(ctx, principal.TenantID, normalized)
		if err != nil {
			return err
		}
		candidates = found
	} else {
		// 未实现通道仓储时退回租户级查重：宁可更严格，也不能放过同名重复建档。
		duplicates, err := s.repo.FindDuplicates(ctx, principal.TenantID, normalized, "", 0)
		if err != nil {
			return err
		}
		for _, duplicate := range duplicates {
			candidates = append(candidates, ProvisionCandidate{ID: duplicate.ID, Name: duplicate.Name, Status: duplicate.Status, Enabled: duplicate.Status == StatusActive})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return apperror.WithDetails(ErrProvisionCustomerAmbiguous, provisionAmbiguityDetail{Name: name, Candidates: candidates})
}

// buildProvisionCustomer 组装待写入客户模型。
// 业务操作者取 X-Actor-Identity-ID（审计归属），负责人取已校验的 owner_identity_id。
func (s *Service) buildProvisionCustomer(principal auth.Principal, request ProvisionCreateRequest, ownerOrgID string) (*Customer, error) {
	model := &Customer{Name: request.Name, NormalizedName: normalizeName(request.Name), CustomerType: request.CustomerType, Industry: request.Industry, Region: request.Region,
		OwnerUserID: request.OwnerIdentityID, OwnerOrgID: ownerOrgID, Status: StatusActive, CreditLevel: "B", CreditChangeSource: "INITIAL"}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, request.ActorIdentityID, request.ActorIdentityID, 1
	for index, item := range request.Contacts {
		phoneCipher, err := s.codec.Encrypt(item.Phone)
		if err != nil {
			return nil, err
		}
		emailCipher, err := s.codec.Encrypt(item.Email)
		if err != nil {
			return nil, err
		}
		model.Contacts = append(model.Contacts, Contact{
			Model: database.Model{TenantID: principal.TenantID, CreatedBy: request.ActorIdentityID, UpdatedBy: request.ActorIdentityID, Version: 1},
			Name:  item.Name, PhoneCipher: phoneCipher, PhoneMasked: security.MaskPhone(item.Phone), EmailCipher: emailCipher,
			EmailMasked: maskEmail(item.Email), IsRegistration: item.IsRegistration, SortOrder: index,
		})
	}
	return model, nil
}

// persistProvisionCustomer 在调用方事务内完成编号分配、落库与来源审计。
// 审计 detail 承载 origin 与 external_record_id：客户表没有来源列，
// 定稿允许用审计承载来源标记，因此不新增迁移。
func (s *Service) persistProvisionCustomer(ctx context.Context, principal auth.Principal, model *Customer, request ProvisionCreateRequest) error {
	number, err := s.repo.NextNumber(ctx, principal.TenantID, businessclock.DateString(s.now()))
	if err != nil {
		return err
	}
	model.CustomerNo = number
	if err = s.repo.Create(ctx, model); err != nil {
		return mapWriteError(err)
	}
	snapshot := provisionCustomerSnapshot{
		Origin: ProvisionOrigin, ExternalRecordID: request.ExternalRecordID,
		ActorIdentityID: request.ActorIdentityID, Customer: toResponse(model),
	}
	return s.audit.Write(ctx, audit.Event{
		TenantID: principal.TenantID, Module: "customer", Operation: "IMPORT_PROVISION_CREATE", ResourceType: "customer",
		ResourceID: stringUint(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName,
		AfterJSON: audit.JSON(snapshot), Reason: request.Reason, Result: "SUCCESS",
	})
}

// replayProvisionCreate 校验幂等坐标并回放既有结果。
// 与交互式创建不同：业务记录的 created_by 是操作者平台身份（X-Actor-Identity-ID），
// 幂等表的 actor_id 是机器凭据主体，两者都必须在重放时精确匹配。
func (s *Service) replayProvisionCreate(ctx context.Context, principal auth.Principal, request ProvisionCreateRequest, requestHash string, prior *CreateIdempotency) (*Response, error) {
	if prior == nil || prior.TenantID != principal.TenantID || prior.ActorID != principal.UserID || prior.Key != request.IdempotencyKey || prior.RequestHash != requestHash || prior.Status != "COMPLETED" {
		return nil, ErrIdempotencyConflict
	}
	resource, err := s.create.FindCreatedCustomer(ctx, principal.TenantID, request.ActorIdentityID, prior.CustomerID)
	if err != nil {
		return nil, ErrCreateReplayInvalid
	}
	if resource.ID != prior.CustomerID || resource.TenantID != principal.TenantID {
		return nil, ErrCreateReplayInvalid
	}
	if responseDigest(prior.ResponseJSON) != prior.ResponseHash {
		return nil, ErrCreateReplayInvalid
	}
	var response Response
	if err = json.Unmarshal(prior.ResponseJSON, &response); err != nil || response.ID != prior.CustomerID || response.CustomerNo != resource.CustomerNo {
		return nil, ErrCreateReplayInvalid
	}
	return &response, nil
}

// provisionCreateRequestHash 绑定租户、机器主体、操作者身份、来源与业务字段，
// 使同一幂等键不能被换载荷（换负责人、换来源记录）复用。
func (s *Service) provisionCreateRequestHash(principal auth.Principal, request ProvisionCreateRequest, ownerOrgID string) (string, error) {
	type canonicalContact struct {
		Name           string `json:"name"`
		PhoneHMAC      string `json:"phone_hmac"`
		EmailHMAC      string `json:"email_hmac"`
		IsRegistration bool   `json:"is_registration"`
	}
	contacts := make([]canonicalContact, 0, len(request.Contacts))
	for _, contact := range request.Contacts {
		contacts = append(contacts, canonicalContact{Name: contact.Name, PhoneHMAC: s.codec.HMAC(contact.Phone), EmailHMAC: s.codec.HMAC(contact.Email), IsRegistration: contact.IsRegistration})
	}
	canonical := struct {
		TenantID, ActorID, ActorIdentityID, Origin, ExternalRecordID              string
		Name, CustomerType, Industry, Region, OwnerIdentityID, OwnerOrgID, Reason string
		Contacts                                                                  []canonicalContact
	}{
		TenantID: principal.TenantID, ActorID: principal.UserID, ActorIdentityID: request.ActorIdentityID,
		Origin: ProvisionOrigin, ExternalRecordID: request.ExternalRecordID,
		Name: request.Name, CustomerType: request.CustomerType, Industry: request.Industry, Region: request.Region,
		OwnerIdentityID: request.OwnerIdentityID, OwnerOrgID: ownerOrgID, Reason: request.Reason, Contacts: contacts,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// provisionLookupResourceID 为只读查询生成稳定的审计资源标识（名称归一化摘要），
// 使同一客户名的查询在审计里可聚合，同时不把客户名当作资源主键。
func provisionLookupResourceID(normalizedName string) string {
	digest := sha256.Sum256([]byte(ProvisionOrigin + "\x00" + normalizedName))
	return "lookup-" + hex.EncodeToString(digest[:16])
}

// FindProvisionCandidates 按归一化名称全量列出候选客户（含停用与已合并）。
// 参数：ctx 为请求上下文，tenantID 为租户，normalizedName 为归一化名称。
// 返回值：候选摘要（最多 21 条，用于识别“多命中”），数据库错误原样返回。
func (r *GORMRepository) FindProvisionCandidates(ctx context.Context, tenantID, normalizedName string) ([]ProvisionCandidate, error) {
	type row struct {
		ID           uint64
		Name         string
		Status       string
		MergedIntoID *uint64
	}
	var rows []row
	// 名称唯一性判定必须覆盖停用与已合并客户：它们同样占用名称，不能再建同名的活跃客户。
	err := database.FromContext(ctx, r.db).Table("crm_customers").
		Select("id, name, status, merged_into_id").
		Where("tenant_id=? AND normalized_name=? AND deleted_at IS NULL", tenantID, normalizedName).
		Order("id").Limit(21).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	candidates := make([]ProvisionCandidate, 0, len(rows))
	for _, item := range rows {
		candidates = append(candidates, ProvisionCandidate{ID: item.ID, Name: item.Name, Status: item.Status,
			Enabled: item.Status == StatusActive && item.MergedIntoID == nil})
	}
	return candidates, nil
}

// provisionLookupSnapshot 是查询审计的可序列化快照，含 origin 与判定结果。
type provisionLookupSnapshot struct {
	Origin         string               `json:"origin"`
	NormalizedName string               `json:"normalized_name"`
	Unique         bool                 `json:"unique"`
	Candidates     []ProvisionCandidate `json:"candidates"`
}

// provisionCustomerSnapshot 是建档审计的可序列化快照，含 origin 与来源记录标识。
type provisionCustomerSnapshot struct {
	Origin           string   `json:"origin"`
	ExternalRecordID string   `json:"external_record_id,omitempty"`
	ActorIdentityID  string   `json:"actor_identity_id"`
	Customer         Response `json:"customer"`
}

// provisionAmbiguityDetail 是名称冲突时返回给合同侧的候选明细。
type provisionAmbiguityDetail struct {
	Name       string               `json:"name"`
	Candidates []ProvisionCandidate `json:"candidates"`
}

var _ ProvisionDirectory = (*GORMRepository)(nil)
