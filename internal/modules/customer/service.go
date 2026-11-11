package customer

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/businessclock"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/gorm"
)

type Service struct {
	freeze        FreezeChecker
	db            *gorm.DB
	repo          Repository
	create        CreateRepository
	merge         MergeRepository
	profile       ProfileRepository
	imports       ImportRepository
	audit         audit.Writer
	codec         *security.SensitiveCodec
	scanner       ImportFileScanner
	projects      ProjectHistoryReader
	owners        ownerdirectory.Catalog
	importGateway ImportFileGateway
	importSources ImportSourceRepository
	now           func() time.Time
	// 更新事务在生产环境绑定 GORM 事务；保留该接缝以便聚焦测试用假仓储验证完整更新路径，
	// 与 opportunity.Service 的事务接缝保持同一约定。
	updateTransaction func(context.Context, func(context.Context) error) error
}

type ImportFileGateway interface {
	StoreImport(context.Context, string, string, string, []byte) error
}

func NewService(db *gorm.DB, repo Repository, auditWriter audit.Writer, codec *security.SensitiveCodec) *Service {
	service := &Service{db: db, repo: repo, audit: auditWriter, codec: codec, now: func() time.Time { return time.Now().UTC() }}
	service.updateTransaction = func(ctx context.Context, fn func(context.Context) error) error {
		return database.WithTransaction(ctx, db, fn)
	}
	// XLSX 安全边界由进程内的有界 ZIP/OOXML 校验和业务字段校验承担。
	// 外部杀毒引擎不是客户导入可用性的前置条件。
	service.scanner = CodeImportScanner{}
	if create, ok := repo.(CreateRepository); ok {
		service.create = create
	}
	if merge, ok := repo.(MergeRepository); ok {
		service.merge = merge
	}
	if profile, ok := repo.(ProfileRepository); ok {
		service.profile = profile
	}
	if imports, ok := repo.(ImportRepository); ok {
		service.imports = imports
	}
	if sources, ok := repo.(ImportSourceRepository); ok {
		service.importSources = sources
	}
	return service
}

// UseImportScanner 保留扩展点以兼容已有调用方；默认使用 CodeImportScanner。
func (s *Service) UseImportScanner(scanner ImportFileScanner) *Service {
	if scanner != nil {
		s.scanner = scanner
	}
	return s
}

func (s *Service) UseImportFileGateway(gateway ImportFileGateway) *Service {
	s.importGateway = gateway
	return s
}

func (s *Service) UseProjectHistoryReader(reader ProjectHistoryReader) *Service {
	s.projects = reader
	return s
}

// 负责人和组织必须作为一对交给基础平台权威目录校验，不能分别验证后自行拼接。
// 未配置正式集成合同时保留既有业务读写，但负责人选择目录明确报告依赖不可用。
func (s *Service) UseOwnerDirectory(catalog ownerdirectory.Catalog) *Service {
	s.owners = catalog
	return s
}

func (s *Service) CheckDuplicate(ctx context.Context, request DuplicateCheckRequest) ([]DuplicateCandidate, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// 重复检查会直接返回客户名称和编号；销售只能枚举自己创建的候选，
	// 而创建/导入时仍保留租户级校验，避免因可见性收窄破坏唯一性约束。
	if scopedRepo, ok := s.repo.(ScopedDuplicateRepository); ok {
		return scopedRepo.FindDuplicatesScoped(ctx, principal, normalizeName(request.Name), s.codec.HMAC(request.UnifiedCreditCode), 0)
	}
	return s.repo.FindDuplicates(ctx, principal.TenantID, normalizeName(request.Name), s.codec.HMAC(request.UnifiedCreditCode), 0)
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (*Response, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.HasPermission("customer.create") {
		return nil, apperror.ErrForbidden
	}
	if s.create == nil {
		return nil, ErrCreateIdempotencyUnavailable
	}
	request = inheritCreateOwner(normalizeCreateRequest(request), principal)
	if err := s.ensureOwnerNotFrozen(ctx, principal.TenantID, request.OwnerUserID); err != nil {
		return nil, err
	}
	if request.Reason == "" {
		request.Reason = "创建客户档案"
	}
	if err := validateCustomerMasterData(request.Name, request.CustomerType, request.Industry, request.Region, request.Reason); err != nil {
		return nil, err
	}
	if s.owners != nil {
		if request.OwnerOrgID == "" {
			page, listErr := s.owners.List(ctx, ownerdirectory.Query{UserID: request.OwnerUserID, Page: 1, PageSize: 1})
			if listErr != nil {
				return nil, listErr
			}
			request.OwnerOrgID = ownerdirectory.PrimaryOrganization(page, request.OwnerUserID)
		}
		if err = s.owners.Validate(ctx, request.OwnerUserID, request.OwnerOrgID); err != nil {
			return nil, err
		}
	}
	if request.IdempotencyKey == "" {
		return nil, ErrIdempotencyRequired
	}
	if len(request.IdempotencyKey) > 128 {
		return nil, ErrIdempotencyInvalid
	}
	if request.DuplicateOverride && !principal.HasPermission("customer.duplicate.override") {
		return nil, apperror.ErrForbidden
	}
	if err = validateRegistrationContacts(request.Contacts); err != nil {
		return nil, err
	}
	// 摘要绑定租户、操作者、标准化业务字段以及敏感字段 HMAC；同一幂等键不能被换载荷复用，
	// 同时避免把信用代码、电话和邮箱明文写入幂等表。
	requestHash, err := s.createRequestHash(principal, request)
	if err != nil {
		return nil, err
	}
	if prior, findErr := s.create.FindCreateIdempotency(ctx, principal.TenantID, principal.UserID, request.IdempotencyKey); findErr != nil {
		return nil, findErr
	} else if prior != nil {
		return s.replayCreate(ctx, principal, request, requestHash, prior)
	}
	if err = s.validateCreateDuplicates(ctx, principal, request, 0); err != nil {
		return nil, err
	}
	creditCipher, err := s.codec.Encrypt(request.UnifiedCreditCode)
	if err != nil {
		return nil, err
	}
	var creditHMAC *string
	if request.UnifiedCreditCode != "" {
		value := s.codec.HMAC(request.UnifiedCreditCode)
		creditHMAC = &value
	}
	model := &Customer{Name: strings.TrimSpace(request.Name), NormalizedName: normalizeName(request.Name), UnifiedCreditCodeCipher: creditCipher, UnifiedCreditCodeHMAC: creditHMAC, CustomerType: request.CustomerType, Industry: request.Industry, Region: request.Region, OwnerUserID: request.OwnerUserID, OwnerOrgID: request.OwnerOrgID, Status: StatusActive, CreditLevel: "B", CreditChangeSource: "INITIAL"}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, principal.UserID, principal.UserID, 1
	for index, item := range request.Contacts {
		phoneCipher, encryptErr := s.codec.Encrypt(item.Phone)
		if encryptErr != nil {
			return nil, encryptErr
		}
		emailCipher, encryptErr := s.codec.Encrypt(item.Email)
		if encryptErr != nil {
			return nil, encryptErr
		}
		model.Contacts = append(model.Contacts, Contact{Model: database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, Version: 1}, Name: item.Name, PhoneCipher: phoneCipher, PhoneMasked: security.MaskPhone(item.Phone), EmailCipher: emailCipher, EmailMasked: maskEmail(item.Email), IsRegistration: item.IsRegistration, SortOrder: index})
	}
	var response *Response
	// 事务内再次检查幂等记录与重复客户，关闭“事务外预检后并发插入”的时间窗口。
	err = s.create.WithCreateTransaction(ctx, func(txCtx context.Context) error {
		prior, findErr := s.create.FindCreateIdempotency(txCtx, principal.TenantID, principal.UserID, request.IdempotencyKey)
		if findErr != nil {
			return findErr
		}
		if prior != nil {
			var replayErr error
			response, replayErr = s.replayCreate(txCtx, principal, request, requestHash, prior)
			return replayErr
		}
		if duplicateErr := s.validateCreateDuplicates(txCtx, principal, request, 0); duplicateErr != nil {
			return duplicateErr
		}
		// 事务内最终防线：事务外的预检只用于早失败，冻结可能在事务开启后落库。
		if guardErr := s.guardOwnerWrite(txCtx, principal.TenantID, request.OwnerUserID); guardErr != nil {
			return guardErr
		}
		if persistErr := s.persistCreatedCustomer(txCtx, principal, model, request.Reason); persistErr != nil {
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
				return s.replayCreate(ctx, principal, request, requestHash, prior)
			}
		}
		return nil, err
	}
	return response, nil
}

func normalizeCreateRequest(input CreateRequest) CreateRequest {
	input.Name = strings.TrimSpace(input.Name)
	input.UnifiedCreditCode = strings.TrimSpace(input.UnifiedCreditCode)
	input.CustomerType = strings.TrimSpace(input.CustomerType)
	input.Industry = strings.TrimSpace(input.Industry)
	input.Region = strings.TrimSpace(input.Region)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OwnerOrgID = strings.TrimSpace(input.OwnerOrgID)
	input.DuplicateOverrideReason = strings.TrimSpace(input.DuplicateOverrideReason)
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	for index := range input.Contacts {
		input.Contacts[index].Name = strings.TrimSpace(input.Contacts[index].Name)
		input.Contacts[index].Phone = strings.TrimSpace(input.Contacts[index].Phone)
		input.Contacts[index].Email = strings.TrimSpace(input.Contacts[index].Email)
	}
	return input
}

// inheritCreateOwner 把新客户归属绑定到可信认证主体。请求中的负责人字段只为旧客户端
// 保持反序列化兼容，不能改变新客户的初始负责人或组织归属。
func inheritCreateOwner(input CreateRequest, principal auth.Principal) CreateRequest {
	input.OwnerUserID = strings.TrimSpace(principal.UserID)
	input.OwnerOrgID = strings.TrimSpace(principal.PrimaryOrgID)
	return input
}

func (s *Service) createRequestHash(principal auth.Principal, input CreateRequest) (string, error) {
	type canonicalContact struct {
		Name           string `json:"name"`
		PhoneHMAC      string `json:"phone_hmac"`
		EmailHMAC      string `json:"email_hmac"`
		IsRegistration bool   `json:"is_registration"`
	}
	contacts := make([]canonicalContact, 0, len(input.Contacts))
	for _, contact := range input.Contacts {
		contacts = append(contacts, canonicalContact{
			Name: contact.Name, PhoneHMAC: s.codec.HMAC(contact.Phone),
			EmailHMAC: s.codec.HMAC(contact.Email), IsRegistration: contact.IsRegistration,
		})
	}
	canonical := struct {
		TenantID, ActorID, Name, UnifiedCreditCodeHMAC          string
		CustomerType, Industry, Region, OwnerUserID, OwnerOrgID string
		DuplicateOverride                                       bool
		DuplicateOverrideReason, Reason                         string
		Contacts                                                []canonicalContact
	}{
		TenantID: principal.TenantID, ActorID: principal.UserID, Name: input.Name,
		UnifiedCreditCodeHMAC: s.codec.HMAC(input.UnifiedCreditCode), CustomerType: input.CustomerType,
		Industry: input.Industry, Region: input.Region, OwnerUserID: input.OwnerUserID, OwnerOrgID: input.OwnerOrgID,
		DuplicateOverride: input.DuplicateOverride, DuplicateOverrideReason: input.DuplicateOverrideReason,
		Reason: input.Reason, Contacts: contacts,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) validateCreateDuplicates(ctx context.Context, principal auth.Principal, request CreateRequest, excludeID uint64) error {
	normalizedName, creditHMAC := normalizeName(request.Name), s.codec.HMAC(request.UnifiedCreditCode)
	duplicates, err := s.repo.FindDuplicates(ctx, principal.TenantID, normalizedName, creditHMAC, excludeID)
	if err != nil {
		return err
	}
	for _, candidate := range duplicates {
		if candidate.ExactCode {
			return s.duplicateConflictError(ctx, principal, normalizedName, creditHMAC, excludeID, duplicates, ErrDuplicateCode)
		}
	}
	if len(duplicates) > 0 && (!request.DuplicateOverride || !principal.HasPermission("customer.duplicate.override") || request.DuplicateOverrideReason == "") {
		return s.duplicateConflictError(ctx, principal, normalizedName, creditHMAC, excludeID, duplicates, ErrDuplicateName)
	}
	return nil
}

// duplicateConflictError 保留租户级唯一性判定，但错误明细只包含调用者数据范围内
// 的候选；范围外命中只返回受限冲突和需管理员处理的提示，不披露名称、编号、状态或 ID。
func (s *Service) duplicateConflictError(ctx context.Context, principal auth.Principal, normalizedName, creditHMAC string, excludeID uint64, tenantDuplicates []DuplicateCandidate, base *apperror.Error) error {
	visible := make([]DuplicateCandidate, 0)
	// 仓储未实现可见范围扩展点时不回退到租户全量明细，宁可只给安全冲突提示。
	if scoped, ok := s.repo.(ScopedDuplicateRepository); ok {
		result, err := scoped.FindDuplicatesScoped(ctx, principal, normalizedName, creditHMAC, excludeID)
		if err != nil {
			return err
		}
		visible = append(visible, result...)
	}
	visibleIDs := make(map[uint64]struct{}, len(visible))
	for _, candidate := range visible {
		visibleIDs[candidate.ID] = struct{}{}
	}
	restricted := false
	for _, candidate := range tenantDuplicates {
		if _, exists := visibleIDs[candidate.ID]; !exists {
			restricted = true
			break
		}
	}
	// 保留基础错误作为 Cause，调用方仍可用 errors.Is 判断冲突类型；明细另行附加。
	conflict := *base
	conflict.Details = DuplicateConflictDetails{
		VisibleCandidates:   visible,
		RestrictedConflict:  restricted,
		AdminReviewRequired: restricted,
	}
	conflict.Cause = base
	return &conflict
}

func (s *Service) replayCreate(ctx context.Context, principal auth.Principal, request CreateRequest, requestHash string, prior *CreateIdempotency) (*Response, error) {
	if prior == nil || prior.TenantID != principal.TenantID || prior.ActorID != principal.UserID || prior.Key != request.IdempotencyKey || prior.RequestHash != requestHash || prior.Status != "COMPLETED" {
		return nil, ErrIdempotencyConflict
	}
	resource, err := s.create.FindCreatedCustomer(ctx, principal.TenantID, principal.UserID, prior.CustomerID)
	if err != nil {
		return nil, ErrCreateReplayInvalid
	}
	if resource.ID != prior.CustomerID || resource.TenantID != principal.TenantID || resource.CreatedBy != principal.UserID {
		return nil, ErrCreateReplayInvalid
	}
	var response Response
	if responseDigest(prior.ResponseJSON) != prior.ResponseHash {
		return nil, ErrCreateReplayInvalid
	}
	if err = json.Unmarshal(prior.ResponseJSON, &response); err != nil || response.ID != prior.CustomerID || response.CustomerNo != resource.CustomerNo {
		return nil, ErrCreateReplayInvalid
	}
	return &response, nil
}

func responseDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func isCustomerCreateRaceCandidate(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false
	}
	// 两个携带统一信用代码的相同并发请求，失败方可能先撞到客户唯一索引，尚未来得及写入幂等表。
	// 因此两类已知唯一键冲突都可尝试查找同操作者的胜出记录；只有租户、操作者、键、规范化载荷、
	// 资源和响应快照全部吻合才按重放返回，否则保留原始业务重复错误。
	message := strings.ToLower(mysqlErr.Message)
	return strings.Contains(message, "uq_customer_create_idempotency") ||
		strings.Contains(message, "uk_customer_credit")
}

// persistCreatedCustomer 必须在调用方持有的事务内执行。交互创建和导入的每个独立提交行共用它，
// 使编号分配、数据落库和脱敏审计不会形成两套不同语义。
func (s *Service) persistCreatedCustomer(ctx context.Context, principal auth.Principal, model *Customer, reason string) error {
	// 编号日期段属于业务日历值，必须按北京时间取日；存储时间戳仍保持 UTC。
	number, err := s.repo.NextNumber(ctx, principal.TenantID, businessclock.DateString(s.now()))
	if err != nil {
		return err
	}
	model.CustomerNo = number
	if err = s.repo.Create(ctx, model); err != nil {
		return mapWriteError(err)
	}
	return s.audit.Write(ctx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "CREATE", ResourceType: "customer", ResourceID: stringUint(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(toResponse(model)), Reason: reason, Result: "SUCCESS"})
}

func (s *Service) runUpdateTransaction(ctx context.Context, fn func(context.Context) error) error {
	if s.updateTransaction != nil {
		return s.updateTransaction(ctx, fn)
	}
	return database.WithTransaction(ctx, s.db, fn)
}

func (s *Service) Update(ctx context.Context, id uint64, request UpdateRequest) (*Response, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	request.Name = strings.TrimSpace(request.Name)
	request.CustomerType = strings.TrimSpace(request.CustomerType)
	request.Industry = strings.TrimSpace(request.Industry)
	request.Region = strings.TrimSpace(request.Region)
	request.OwnerUserID = strings.TrimSpace(request.OwnerUserID)
	request.OwnerOrgID = strings.TrimSpace(request.OwnerOrgID)
	request.Reason = strings.TrimSpace(request.Reason)
	// 联系人集合是可选子资源：只有提交非空数组才表示整表替换。nil（缺省/null）和空数组
	// 都保留数据库既有联系人，因此不能对它们执行“恰好一个注册联系人”的集合校验。
	replaceContacts := request.Contacts != nil && len(*request.Contacts) > 0
	if replaceContacts {
		if err = validateUpdateRegistrationContacts(*request.Contacts); err != nil {
			return nil, err
		}
	}
	if s.owners != nil {
		if err = s.owners.Validate(ctx, request.OwnerUserID, request.OwnerOrgID); err != nil {
			return nil, err
		}
	}
	if err := validateCustomerMasterData(request.Name, request.CustomerType, request.Industry, request.Region, request.Reason); err != nil {
		return nil, err
	}
	current, err := s.repo.FindByID(ctx, principal, id, true)
	if err != nil {
		return nil, err
	}
	// 负责人目录未接线（OWNER_DIRECTORY_ENABLED=false）时必须失败关闭：缺少权威目录就无法
	// 证明目标用户与组织是有效任职关系，任何改变负责人归属的更新都会被拒绝；
	// 不涉及负责人变更的更新保持原行为。
	if s.owners == nil && (current.OwnerUserID != request.OwnerUserID || current.OwnerOrgID != request.OwnerOrgID) {
		return nil, ownerdirectory.ErrUnavailable
	}
	if current.Status != StatusActive {
		return nil, ErrInactive
	}
	if current.Version != request.Version {
		return nil, ErrVersionConflict
	}
	ownerChanged := current.OwnerUserID != request.OwnerUserID
	if ownerChanged {
		if err := s.ensureOwnerNotFrozen(ctx, principal.TenantID, request.OwnerUserID); err != nil {
			return nil, err
		}
	}
	creditHMACValue := ""
	if request.UnifiedCreditCode != nil {
		creditHMACValue = s.codec.HMAC(*request.UnifiedCreditCode)
	} else if current.UnifiedCreditCodeHMAC != nil {
		creditHMACValue = *current.UnifiedCreditCodeHMAC
	}
	normalizedName := normalizeName(request.Name)
	duplicates, err := s.repo.FindDuplicates(ctx, principal.TenantID, normalizedName, creditHMACValue, id)
	if err != nil {
		return nil, err
	}
	for _, candidate := range duplicates {
		if candidate.ExactCode {
			return nil, s.duplicateConflictError(ctx, principal, normalizedName, creditHMACValue, id, duplicates, ErrDuplicateCode)
		}
	}
	if len(duplicates) > 0 && (!request.DuplicateOverride || !principal.HasPermission("customer.duplicate.override") || strings.TrimSpace(request.DuplicateOverrideReason) == "") {
		return nil, s.duplicateConflictError(ctx, principal, normalizedName, creditHMACValue, id, duplicates, ErrDuplicateName)
	}
	creditCipher, creditHMAC := current.UnifiedCreditCodeCipher, current.UnifiedCreditCodeHMAC
	if request.UnifiedCreditCode != nil {
		creditCipher, err = s.codec.Encrypt(strings.TrimSpace(*request.UnifiedCreditCode))
		if err != nil {
			return nil, err
		}
		creditHMAC = nil
		if strings.TrimSpace(*request.UnifiedCreditCode) != "" {
			creditHMAC = &creditHMACValue
		}
	}
	before := toResponse(current)
	current.Name, current.NormalizedName, current.UnifiedCreditCodeCipher, current.UnifiedCreditCodeHMAC = strings.TrimSpace(request.Name), normalizeName(request.Name), creditCipher, creditHMAC
	current.CustomerType, current.Industry, current.Region, current.OwnerUserID, current.OwnerOrgID, current.UpdatedBy = request.CustomerType, request.Industry, request.Region, request.OwnerUserID, request.OwnerOrgID, principal.UserID
	// 更新 DTO 的敏感字段使用指针表达“未提交”；未提交时沿用旧密文，避免前端只拿到脱敏值后
	// 又把掩码覆盖回数据库。全量替换仍通过 ID 校验阻止挂接其他客户的联系人。
	// 未提交联系人（nil 或空数组）时完全跳过这段重建：current.Contacts 保持 FindByID 预加载的
	// 既有行，因此记录 ID、is_registration 标记、version 与审计 diff 都不被改动。
	if replaceContacts {
		existingContacts := make(map[uint64]Contact, len(current.Contacts))
		for _, contact := range current.Contacts {
			existingContacts[contact.ID] = contact
		}
		current.Contacts = nil
		for index, item := range *request.Contacts {
			previous, exists := existingContacts[item.ID]
			if item.ID != 0 && !exists {
				return nil, ErrInvalidContact
			}
			phoneCipher, phoneMasked := previous.PhoneCipher, previous.PhoneMasked
			if item.Phone != nil {
				if strings.TrimSpace(*item.Phone) == "" {
					return nil, ErrInvalidContact
				}
				var encryptErr error
				phoneCipher, encryptErr = s.codec.Encrypt(*item.Phone)
				if encryptErr != nil {
					return nil, encryptErr
				}
				phoneMasked = security.MaskPhone(*item.Phone)
			} else if !exists {
				return nil, ErrInvalidContact
			}
			emailCipher, emailMasked := previous.EmailCipher, previous.EmailMasked
			if item.Email != nil {
				var encryptErr error
				emailCipher, encryptErr = s.codec.Encrypt(*item.Email)
				if encryptErr != nil {
					return nil, encryptErr
				}
				emailMasked = maskEmail(*item.Email)
			}
			current.Contacts = append(current.Contacts, Contact{Model: database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, Version: 1}, CustomerID: id, Name: strings.TrimSpace(item.Name), PhoneCipher: phoneCipher, PhoneMasked: phoneMasked, EmailCipher: emailCipher, EmailMasked: emailMasked, IsRegistration: item.IsRegistration, SortOrder: index})
		}
	}
	err = s.runUpdateTransaction(ctx, func(txCtx context.Context) error {
		// 负责人变更同样在事务内加门闩复核：预检与写入之间可能刚好落库冻结。
		if ownerChanged {
			if guardErr := s.guardOwnerWrite(txCtx, principal.TenantID, request.OwnerUserID); guardErr != nil {
				return guardErr
			}
		}
		if updateErr := s.repo.Update(txCtx, current, request.Version); updateErr != nil {
			return updateErr
		}
		// 只有整表替换才软删除并重建联系人；保留路径不触碰联系人表，避免无意义的重建
		// 造成联系人 ID 漂移和 is_registration 标记丢失。
		if replaceContacts {
			if updateErr := s.repo.ReplaceContacts(txCtx, current); updateErr != nil {
				return updateErr
			}
		}
		for _, log := range buildChangeLogs(principal, id, before, toResponse(current), request.Reason, requestctx.ID(txCtx), s.now()) {
			if updateErr := s.repo.CreateChangeLog(txCtx, &log); updateErr != nil {
				return updateErr
			}
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "UPDATE", ResourceType: "customer", ResourceID: stringUint(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(toResponse(current)), Reason: request.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	result := toResponse(current)
	return &result, nil
}

func buildChangeLogs(principal auth.Principal, customerID uint64, before, after Response, reason, requestID string, occurredAt time.Time) []ChangeLog {
	logs := make([]ChangeLog, 0, 8)
	appendChange := func(field string, oldValue, newValue any) {
		oldJSON, newJSON := audit.JSON(oldValue), audit.JSON(newValue)
		if string(oldJSON) == string(newJSON) {
			return
		}
		logs = append(logs, ChangeLog{TenantID: principal.TenantID, CustomerID: customerID, FieldName: field, BeforeJSON: oldJSON, AfterJSON: newJSON, Reason: reason, OperatorID: principal.UserID, RequestID: requestID, OccurredAt: occurredAt})
	}
	appendChange("name", before.Name, after.Name)
	appendChange("customer_type", before.CustomerType, after.CustomerType)
	appendChange("industry", before.Industry, after.Industry)
	appendChange("region", before.Region, after.Region)
	appendChange("owner_user_id", before.OwnerUserID, after.OwnerUserID)
	appendChange("owner_org_id", before.OwnerOrgID, after.OwnerOrgID)
	// 联系人 DTO 只含脱敏值；密文和明文均不得进入审计 JSON。
	appendChange("contacts", before.Contacts, after.Contacts)
	return logs
}

func (s *Service) Void(ctx context.Context, id uint64, request StatusChangeRequest) (*Response, error) {
	return s.changeStatus(ctx, id, request, StatusVoid)
}

func (s *Service) Restore(ctx context.Context, id uint64, request StatusChangeRequest) (*Response, error) {
	return s.changeStatus(ctx, id, request, StatusActive)
}

func (s *Service) changeStatus(ctx context.Context, id uint64, request StatusChangeRequest, target string) (*Response, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		return nil, ErrChangeReasonRequired
	}
	model, err := s.repo.FindByID(ctx, principal, id, true)
	if err != nil {
		return nil, err
	}
	if model.Version != request.Version {
		return nil, ErrVersionConflict
	}
	if target == StatusVoid {
		if model.Status != StatusActive {
			return nil, ErrInactive
		}
		blockers, checkErr := s.repo.VoidBlockers(ctx, principal.TenantID, id)
		if checkErr != nil {
			return nil, checkErr
		}
		if len(blockers) > 0 {
			return nil, apperror.WithDetails(ErrVoidBlocked, map[string]any{"blockers": blockers})
		}
		// end_date 是业务日历日（DATE 列，前端展示“失效日期”），按北京日历日派生。
		endDate := businessDateValue(s.now())
		model.EndDate = &endDate
	} else {
		if model.Status != StatusVoid {
			return nil, ErrNotVoid
		}
		model.EndDate = nil
	}
	before := toResponse(model)
	model.Status, model.UpdatedBy = target, principal.UserID
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if updateErr := s.repo.UpdateStatus(txCtx, model, request.Version); updateErr != nil {
			return updateErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: target, ResourceType: "customer", ResourceID: stringUint(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(toResponse(model)), Reason: request.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	result := toResponse(model)
	return &result, nil
}

// maxContacts 与 CreateRequest/UpdateRequest 上的 max=50 绑定标签保持一致。服务层再校验一次，
// 覆盖绕过 HTTP 绑定（内部调用、导入、平台通道）的集合，避免一次写入无界子项。
const maxContacts = 50

func validateRegistrationContacts(contacts []ContactInput) error {
	if len(contacts) > maxContacts {
		return ErrContactsLimitExceeded
	}
	for _, contact := range contacts {
		if strings.TrimSpace(contact.Name) == "" || strings.TrimSpace(contact.Phone) == "" {
			return ErrInvalidContact
		}
	}
	count := 0
	for _, contact := range contacts {
		if contact.IsRegistration {
			count++
		}
	}
	if count != 1 {
		return ErrInvalidContact
	}
	return nil
}

func validateUpdateRegistrationContacts(contacts []UpdateContactInput) error {
	if len(contacts) > maxContacts {
		return ErrContactsLimitExceeded
	}
	for _, contact := range contacts {
		if strings.TrimSpace(contact.Name) == "" || (contact.Phone != nil && strings.TrimSpace(*contact.Phone) == "") {
			return ErrInvalidContact
		}
	}
	count := 0
	for _, contact := range contacts {
		if contact.IsRegistration {
			count++
		}
	}
	if count != 1 {
		return ErrInvalidContact
	}
	return nil
}

func validateCustomerMasterData(name, customerType, industry, region, reason string) error {
	if !validCustomerBusinessText(name, 200) || !validCustomerBusinessText(customerType, 64) || !validCustomerBusinessText(industry, 64) || !validCustomerBusinessText(region, 64) || strings.TrimSpace(reason) == "" {
		return ErrInvalidMasterData
	}
	return nil
}

// validCustomerBusinessText 拒绝“1”等占位值，同时保留
// 合法历史值（例如“3M公司”和“制造业”）的可编辑性。
func validCustomerBusinessText(value string, max int) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > max || unsafeText(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) {
			return true
		}
	}
	return false
}

func (s *Service) Get(ctx context.Context, id uint64) (*Response, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindReadableByID(ctx, principal, id, false)
	if err != nil {
		return nil, err
	}
	responses := s.withOwnerDisplayNames(ctx, []Response{toResponse(model)})
	return &responses[0], nil
}

func (s *Service) List(ctx context.Context, query ListQuery) (pagination.Page[Response], error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return pagination.Page[Response]{}, err
	}
	query, err = validateListQuery(query, s.now())
	if err != nil {
		return pagination.Page[Response]{}, err
	}
	if query.QuickFilter == QuickFilterKey {
		return pagination.Page[Response]{}, ErrKeyFilterUnavailable
	}
	page, err := s.repo.List(ctx, principal, query)
	if err != nil {
		return pagination.Page[Response]{}, err
	}
	page.Items = s.withOwnerDisplayNames(ctx, page.Items)
	return page, nil
}

// withOwnerDisplayNames 使用权威目录名称补全读取响应。
// 目录临时故障不得阻塞其他条件已授权的读取。
func (s *Service) withOwnerDisplayNames(ctx context.Context, responses []Response) []Response {
	if s.owners == nil || len(responses) == 0 {
		return responses
	}
	ownerIDs := make([]string, 0, len(responses))
	seen := make(map[string]struct{}, len(responses))
	for _, response := range responses {
		ownerID := strings.TrimSpace(response.OwnerUserID)
		if ownerID == "" {
			continue
		}
		if _, exists := seen[ownerID]; exists {
			continue
		}
		seen[ownerID] = struct{}{}
		ownerIDs = append(ownerIDs, ownerID)
	}
	if len(ownerIDs) == 0 {
		return responses
	}
	owners, err := s.owners.Resolve(ctx, ownerIDs)
	if err != nil {
		return responses
	}
	for index := range responses {
		responses[index].OwnerDisplayName = strings.TrimSpace(owners[responses[index].OwnerUserID].DisplayName)
	}
	return responses
}

func validateListQuery(query ListQuery, now time.Time) (ListQuery, error) {
	query.Keyword = strings.TrimSpace(query.Keyword)
	query.CustomerType = strings.TrimSpace(query.CustomerType)
	query.Industry = strings.TrimSpace(query.Industry)
	query.Region = strings.TrimSpace(query.Region)
	query.OwnerID = strings.TrimSpace(query.OwnerID)
	query.Status = strings.TrimSpace(query.Status)
	query.CreditLevel = strings.ToUpper(strings.TrimSpace(query.CreditLevel))
	if query.CreditLevel != "" && query.CreditLevel != "A" && query.CreditLevel != "B" && query.CreditLevel != "C" && query.CreditLevel != "D" {
		return ListQuery{}, ErrInvalidQuery
	}
	query.QuickFilter = strings.ToUpper(strings.TrimSpace(query.QuickFilter))
	query.SortBy = strings.ToLower(strings.TrimSpace(query.SortBy))
	query.SortOrder = strings.ToLower(strings.TrimSpace(query.SortOrder))
	query.Now = now.UTC()
	if len([]rune(query.Keyword)) > 200 || len(query.CustomerType) > 64 || len(query.Industry) > 64 || len(query.Region) > 64 || len(query.OwnerID) > 64 || len(query.Status) > 32 {
		return ListQuery{}, ErrInvalidQuery
	}
	allowedQuick := map[string]bool{"": true, QuickFilterKey: true, QuickFilterNew: true, QuickFilterWon: true, QuickFilterFollowupDue: true}
	allowedSort := map[string]bool{"": true, "created_at": true, "updated_at": true, "name": true, "last_followup_at": true, "opportunity_amount_sum": true}
	if !allowedQuick[query.QuickFilter] || !allowedSort[query.SortBy] || (query.SortOrder != "" && query.SortOrder != "asc" && query.SortOrder != "desc") {
		return ListQuery{}, ErrInvalidQuery
	}
	if (query.CreatedFrom != nil && query.CreatedTo != nil && !query.CreatedFrom.Before(*query.CreatedTo)) ||
		(query.LastFollowupFrom != nil && query.LastFollowupTo != nil && !query.LastFollowupFrom.Before(*query.LastFollowupTo)) {
		return ListQuery{}, ErrInvalidQuery
	}
	return query, nil
}

func (s *Service) ListContacts(ctx context.Context, id uint64) ([]ContactResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// 联系人属于客户子资源，父客户可见性走只读口径（含 customer.read.all），
	// 但不因此获得任何写权限。
	model, err := s.repo.FindReadableByID(ctx, principal, id, true)
	if err != nil {
		return nil, err
	}
	return toResponse(model).Contacts, nil
}

func (s *Service) ListChangeLogs(ctx context.Context, id uint64, page, pageSize int) (pagination.Page[ChangeLogResponse], error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return pagination.Page[ChangeLogResponse]{}, err
	}
	// 先通过父客户证明当前主体可见，再读取子表，避免只猜测子资源编号形成 IDOR。
	// 可见性按只读口径判定：持有 customer.read.all 的主体可以读取租户内全部客户的历史。
	if _, err = s.repo.FindReadableByID(ctx, principal, id, false); err != nil {
		return pagination.Page[ChangeLogResponse]{}, err
	}
	return s.repo.ListChangeLogs(ctx, principal.TenantID, id, page, pageSize)
}

// ListOpportunityHistory 用途：先验证父客户可见性，再传递完整主体查询商机历史。
// 参数：ctx 为请求；id 为父客户；page、pageSize 为分页。
// 返回值：可见商机分页；客户不可见或查询失败返回错误。
func (s *Service) ListOpportunityHistory(ctx context.Context, id uint64, page, pageSize int) (pagination.Page[OpportunitySummary], error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return pagination.Page[OpportunitySummary]{}, err
	}
	// 父客户可见只证明客户本身可见；商机历史的条目范围必须由商机数据范围再收窄一次，
	// 父客户可见只证明客户本身可见；商机历史的条目范围必须由商机数据范围再收窄一次，
	// 因此 principal 必须继续下传到仓储，不能只传租户。客户全量查看权限只放开父客户校验，
	// 不会放大商机侧的条目范围。
	if _, err = s.repo.FindReadableByID(ctx, principal, id, false); err != nil {
		return pagination.Page[OpportunitySummary]{}, err
	}
	return s.repo.ListOpportunityHistory(ctx, principal, id, page, pageSize)
}

func (s *Service) ProjectHistory(ctx context.Context, id uint64, page, pageSize int) (ProjectHistoryPage, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return ProjectHistoryPage{}, err
	}
	if _, err = s.repo.FindReadableByID(ctx, principal, id, false); err != nil {
		return ProjectHistoryPage{}, err
	}
	if s.projects == nil {
		return ProjectHistoryPage{}, ErrProjectHistoryUnavailable
	}
	value, err := s.projects.ListCustomerProjects(ctx, principal.TenantID, id, page, pageSize)
	if err != nil {
		return ProjectHistoryPage{}, apperror.Wrap(err, ErrProjectHistoryDependency.HTTPStatus, ErrProjectHistoryDependency.Code, ErrProjectHistoryDependency.Message)
	}
	return value, nil
}

// createExportPageSize 是导出内部翻页的固定容量；调用方传入的 Page/PageSize 不生效，
// 避免导出被前端展示用分页参数截断。
const createExportPageSize = 100

// createExportMaxRows 是单次客户导出允许输出的最大数据行数（不含表头）。
// 导出现在会按 Total 一直翻页到末尾，没有上限时一次“全量导出”足以拖垮数据库查询、
// 堆内存和临时文件；超过上限必须整体拒绝，不做静默截断，也不返回部分文件。
const createExportMaxRows = 50000

// createExportRowLimit 是运行时实际生效的上限。生产路径不写它，恒等于 createExportMaxRows；
// 只为让测试注入更小的上限，覆盖“分页累计行数超限”分支，而不必真的构造 5 万行数据。
var createExportRowLimit = createExportMaxRows

// CreateExport 在请求生命周期内生成 CSV 临时文件，保留调用方传入的筛选与排序条件。
// 文件只用于当前响应，调用方必须在 response 完成后删除；不依赖 OSS、导出 Worker
// 或持久化导出任务，也不导出敏感字段。
func (s *Service) CreateExport(ctx context.Context, query ListQuery) (*os.File, error) {
	if _, err := principalFromContext(ctx); err != nil {
		return nil, err
	}
	// 导出自行分页：清空调用方分页参数并固定每页容量，避免把列表页大小带进导出。
	query.Page, query.PageSize = 0, createExportPageSize
	// 未指定排序时沿用 created_at asc。不能让导出落到仓储默认的 updated_at desc：
	// 导出期间数据更新会使行在同一次多页导出里重复或漏出。
	if strings.TrimSpace(query.SortBy) == "" {
		query.SortBy, query.SortOrder = "created_at", "asc"
	}
	file, err := os.CreateTemp("", "crm-customer-export-*.csv")
	if err != nil {
		return nil, err
	}
	cleanup := func(cause error) (*os.File, error) {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		return nil, cause
	}
	// Excel 在中文 Windows 上按本地代码页解析无 BOM 的 UTF-8 CSV，会把中文表头与
	// 客户名称显示成乱码；先写入 UTF-8 BOM，确保 charset=utf-8 的响应能按 UTF-8 打开。
	if _, err = file.WriteString("\xef\xbb\xbf"); err != nil {
		return cleanup(err)
	}
	writer := csv.NewWriter(file)
	if err = writer.Write([]string{"客户编号", "客户名称", "客户类型", "行业", "区域", "负责人姓名", "负责人组织名称", "状态", "创建时间"}); err != nil {
		return cleanup(err)
	}
	// 上限按“已写入数据行”累计，分页过程中数据增删也以累计行数为准；
	// 只要超限就整体失败：已写入的部分既不返回也不静默截断。
	limit := createExportRowLimit
	if limit <= 0 {
		limit = createExportMaxRows
	}
	exported := 0
	for page := 1; ; page++ {
		query.Page = page
		result, listErr := s.List(ctx, query)
		if listErr != nil {
			return cleanup(listErr)
		}
		// List 已带回整批 Total：先在写任何数据行之前拒绝，避免先写入大量内容再回滚。
		if result.Total > int64(limit) {
			return cleanup(newExportRowLimitError(limit))
		}
		owners, resolveErr := s.exportOwnerNames(ctx, result.Items)
		if resolveErr != nil {
			return cleanup(resolveErr)
		}
		for _, item := range result.Items {
			exported++
			if exported > limit {
				return cleanup(newExportRowLimitError(limit))
			}
			owner := owners[item.ID]
			// 导出列包含用户可控文本（客户名称等），统一按 SEC-B6 口径做公式注入转义：
			// =/+/-/@ 前缀与前导 tab/CR 一律加 ' 前缀按字面渲染，普通值原样返回。
			if err = writer.Write([]string{
				safeCSV(item.CustomerNo), safeCSV(item.Name), safeCSV(item.CustomerType), safeCSV(item.Industry), safeCSV(item.Region),
				safeCSV(owner.userName), safeCSV(owner.organizationName), safeCSV(customerStatusLabel(item.Status)), safeCSV(item.CreatedAt.UTC().Format(time.RFC3339)),
			}); err != nil {
				return cleanup(err)
			}
		}
		if len(result.Items) == 0 || int64(page*createExportPageSize) >= result.Total {
			break
		}
	}
	writer.Flush()
	if err = writer.Error(); err != nil {
		return cleanup(err)
	}
	if _, err = file.Seek(0, 0); err != nil {
		return cleanup(err)
	}
	if err = file.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	return file, nil
}

type exportOwnerName struct {
	userName, organizationName string
}

// exportOwnerNames 解析当前权威显示名称。导出
// 文件有意不暴露内部用户或组织标识。
func (s *Service) exportOwnerNames(ctx context.Context, items []Response) (map[uint64]exportOwnerName, error) {
	if s.owners == nil {
		return nil, ownerdirectory.ErrUnavailable
	}
	ownerIDs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		ownerID := strings.TrimSpace(item.OwnerUserID)
		if ownerID == "" {
			continue
		}
		if _, exists := seen[ownerID]; exists {
			continue
		}
		seen[ownerID] = struct{}{}
		ownerIDs = append(ownerIDs, ownerID)
	}
	resolved, err := s.owners.Resolve(ctx, ownerIDs)
	if err != nil {
		return nil, err
	}
	labels := make(map[uint64]exportOwnerName, len(items))
	for _, item := range items {
		user, exists := resolved[strings.TrimSpace(item.OwnerUserID)]
		if !exists {
			continue
		}
		label := exportOwnerName{userName: strings.TrimSpace(user.DisplayName)}
		for _, organization := range user.Organizations {
			if strings.TrimSpace(organization.ID) == strings.TrimSpace(item.OwnerOrgID) {
				label.organizationName = strings.TrimSpace(organization.Name)
				break
			}
		}
		labels[item.ID] = label
	}
	return labels, nil
}

func customerStatusLabel(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case StatusActive:
		return "正常"
	case StatusVoid:
		return "已作废"
	case StatusMerged:
		return "已合并"
	default:
		return "未知"
	}
}

// businessDateValue 把时刻折算成“业务日历日”的 DATE 存储值：取北京日历日的年月日，
// 再以 UTC 午夜表示。end_date 是 DATE 列（前端按“失效日期”展示），只保留年月日；
// 但驱动写库前会先按连接 loc（生产 DSN 是 loc=UTC）换算时间，直接写北京午夜会被写成前一天。
func businessDateValue(value time.Time) time.Time {
	year, month, day := value.In(businessclock.Location()).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func (s *Service) CreateFollowup(ctx context.Context, id uint64, input FollowupCreateRequest) (*FollowupResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return nil, apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_FOLLOWUP_CONTENT_REQUIRED", "followup content is required")
	}
	model := &Followup{CustomerID: id, Type: input.Type, Content: html.EscapeString(content), FollowedAt: input.FollowedAt.UTC(), FollowedBy: principal.UserID, NextFollowAt: input.NextFollowAt}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, principal.UserID, principal.UserID, 1
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if _, lockErr := s.repo.LockActiveForWrite(txCtx, principal, id); lockErr != nil {
			return lockErr
		}
		if createErr := s.repo.CreateFollowup(txCtx, model); createErr != nil {
			return createErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "FOLLOWUP_CREATE", ResourceType: "customer", ResourceID: stringUint(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(toFollowupResponse(model)), Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	result := toFollowupResponse(model)
	return &result, nil
}

func (s *Service) ListFollowups(ctx context.Context, id uint64, page, pageSize int) (pagination.Page[FollowupResponse], error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	// 跟进记录是只读子资源：父客户可见性按只读口径判定，写路径（CreateFollowup）仍走行锁。
	if _, err = s.repo.FindReadableByID(ctx, principal, id, false); err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	return s.repo.ListFollowups(ctx, principal.TenantID, id, page, pageSize)
}

func principalFromContext(ctx context.Context) (auth.Principal, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok || principal.TenantID == "" || principal.UserID == "" {
		return auth.Principal{}, apperror.ErrUnauthenticated
	}
	return principal, nil
}

func mapWriteError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		return apperror.Wrap(err, http.StatusConflict, "CRM_CUSTOMER_DUPLICATE", "customer conflicts with an existing record")
	}
	return err
}

func maskEmail(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	local := []rune(parts[0])
	return string(local[:1]) + "***@" + parts[1]
}
