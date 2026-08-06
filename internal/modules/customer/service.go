package customer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/gorm"
)

type Service struct {
	db       *gorm.DB
	repo     Repository
	create   CreateRepository
	merge    MergeRepository
	profile  ProfileRepository
	imports  ImportRepository
	audit    audit.Writer
	codec    *security.SensitiveCodec
	scanner  ImportFileScanner
	projects ProjectHistoryReader
	owners   ownerdirectory.Catalog
	now      func() time.Time
}

func NewService(db *gorm.DB, repo Repository, auditWriter audit.Writer, codec *security.SensitiveCodec) *Service {
	service := &Service{db: db, repo: repo, audit: auditWriter, codec: codec, now: func() time.Time { return time.Now().UTC() }}
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
	return service
}

// 导入文件必须先经过部署侧恶意内容扫描器；未显式配置该信任边界时保持功能不可用，
// 不能因为本地解析器能打开文件就把它当作安全输入。
func (s *Service) UseImportScanner(scanner ImportFileScanner) *Service {
	s.scanner = scanner
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
	if s.owners != nil {
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
		return nil, ErrInvalidContact
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
	model := &Customer{Name: strings.TrimSpace(request.Name), NormalizedName: normalizeName(request.Name), UnifiedCreditCodeCipher: creditCipher, UnifiedCreditCodeHMAC: creditHMAC, CustomerType: request.CustomerType, Industry: request.Industry, Region: request.Region, OwnerUserID: request.OwnerUserID, OwnerOrgID: request.OwnerOrgID, Status: StatusActive}
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
	duplicates, err := s.repo.FindDuplicates(ctx, principal.TenantID, normalizeName(request.Name), s.codec.HMAC(request.UnifiedCreditCode), excludeID)
	if err != nil {
		return err
	}
	for _, candidate := range duplicates {
		if candidate.ExactCode {
			return apperror.WithDetails(ErrDuplicateCode, duplicates)
		}
	}
	if len(duplicates) > 0 && (!request.DuplicateOverride || !principal.HasPermission("customer.duplicate.override") || request.DuplicateOverrideReason == "") {
		return apperror.WithDetails(ErrDuplicateName, duplicates)
	}
	return nil
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
	number, err := s.repo.NextNumber(ctx, principal.TenantID, s.now().Format("20060102"))
	if err != nil {
		return err
	}
	model.CustomerNo = number
	if err = s.repo.Create(ctx, model); err != nil {
		return mapWriteError(err)
	}
	return s.audit.Write(ctx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "CREATE", ResourceType: "customer", ResourceID: stringUint(model.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(toResponse(model)), Reason: reason, Result: "SUCCESS"})
}

func (s *Service) Update(ctx context.Context, id uint64, request UpdateRequest) (*Response, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err = validateUpdateRegistrationContacts(request.Contacts); err != nil {
		return nil, err
	}
	request.OwnerUserID = strings.TrimSpace(request.OwnerUserID)
	request.OwnerOrgID = strings.TrimSpace(request.OwnerOrgID)
	if s.owners != nil {
		if err = s.owners.Validate(ctx, request.OwnerUserID, request.OwnerOrgID); err != nil {
			return nil, err
		}
	}
	current, err := s.repo.FindByID(ctx, principal, id, true)
	if err != nil {
		return nil, err
	}
	if current.Status != StatusActive {
		return nil, ErrInactive
	}
	if current.Version != request.Version {
		return nil, ErrVersionConflict
	}
	creditHMACValue := ""
	if request.UnifiedCreditCode != nil {
		creditHMACValue = s.codec.HMAC(*request.UnifiedCreditCode)
	} else if current.UnifiedCreditCodeHMAC != nil {
		creditHMACValue = *current.UnifiedCreditCodeHMAC
	}
	duplicates, err := s.repo.FindDuplicates(ctx, principal.TenantID, normalizeName(request.Name), creditHMACValue, id)
	if err != nil {
		return nil, err
	}
	for _, candidate := range duplicates {
		if candidate.ExactCode {
			return nil, apperror.WithDetails(ErrDuplicateCode, duplicates)
		}
	}
	if len(duplicates) > 0 && (!request.DuplicateOverride || !principal.HasPermission("customer.duplicate.override") || strings.TrimSpace(request.DuplicateOverrideReason) == "") {
		return nil, apperror.WithDetails(ErrDuplicateName, duplicates)
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
	existingContacts := make(map[uint64]Contact, len(current.Contacts))
	for _, contact := range current.Contacts {
		existingContacts[contact.ID] = contact
	}
	current.Contacts = nil
	for index, item := range request.Contacts {
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
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if updateErr := s.repo.Update(txCtx, current, request.Version); updateErr != nil {
			return updateErr
		}
		if updateErr := s.repo.ReplaceContacts(txCtx, current); updateErr != nil {
			return updateErr
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
		now := s.now()
		model.EndDate = &now
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

func validateRegistrationContacts(contacts []ContactInput) error {
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

func (s *Service) Get(ctx context.Context, id uint64) (*Response, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id, false)
	if err != nil {
		return nil, err
	}
	response := toResponse(model)
	return &response, nil
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
	return s.repo.List(ctx, principal, query)
}

func validateListQuery(query ListQuery, now time.Time) (ListQuery, error) {
	query.Keyword = strings.TrimSpace(query.Keyword)
	query.CustomerType = strings.TrimSpace(query.CustomerType)
	query.Industry = strings.TrimSpace(query.Industry)
	query.Region = strings.TrimSpace(query.Region)
	query.OwnerID = strings.TrimSpace(query.OwnerID)
	query.Status = strings.TrimSpace(query.Status)
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
	model, err := s.repo.FindByID(ctx, principal, id, true)
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
	if _, err = s.repo.FindByID(ctx, principal, id, false); err != nil {
		return pagination.Page[ChangeLogResponse]{}, err
	}
	return s.repo.ListChangeLogs(ctx, principal.TenantID, id, page, pageSize)
}

func (s *Service) ListOpportunityHistory(ctx context.Context, id uint64, page, pageSize int) (pagination.Page[OpportunitySummary], error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return pagination.Page[OpportunitySummary]{}, err
	}
	if _, err = s.repo.FindByID(ctx, principal, id, false); err != nil {
		return pagination.Page[OpportunitySummary]{}, err
	}
	return s.repo.ListOpportunityHistory(ctx, principal.TenantID, id, page, pageSize)
}

func (s *Service) ProjectHistory(ctx context.Context, id uint64, page, pageSize int) (ProjectHistoryPage, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return ProjectHistoryPage{}, err
	}
	if _, err = s.repo.FindByID(ctx, principal, id, false); err != nil {
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

func (s *Service) CreateExport(ctx context.Context) error {
	if _, err := principalFromContext(ctx); err != nil {
		return err
	}
	return ErrExportUnavailable
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
	if _, err = s.repo.FindByID(ctx, principal, id, false); err != nil {
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
