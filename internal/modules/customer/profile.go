package customer

import (
	"context"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
)

const (
	maxStakeholders       = 100
	maxInformationSystems = 200
)

var stakeholderInfluences = map[string]struct{}{"LOW": {}, "MEDIUM": {}, "HIGH": {}}
var systemProtectionLevels = map[string]struct{}{"LEVEL_1": {}, "LEVEL_2": {}, "LEVEL_3": {}, "LEVEL_4": {}, "LEVEL_5": {}}
var systemFilingStatuses = map[string]struct{}{"NOT_FILED": {}, "FILING": {}, "FILED": {}}

type ProfileRepository interface {
	LockActiveCustomerForProfile(context.Context, auth.Principal, uint64) (*Customer, error)
	FindByID(context.Context, auth.Principal, uint64, bool) (*Customer, error)
	ListStakeholders(context.Context, string, uint64) ([]Stakeholder, error)
	ReplaceStakeholders(context.Context, string, uint64, string, []Stakeholder) error
	ListInformationSystems(context.Context, string, uint64) ([]InformationSystem, error)
	ReplaceInformationSystems(context.Context, string, uint64, string, []InformationSystem) error
	IncrementProfileVersion(context.Context, string, uint64, uint64, string) error
	CreateChangeLog(context.Context, *ChangeLog) error
}

func (s *Service) ListStakeholders(ctx context.Context, customerID uint64) (*StakeholderCollectionResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// 查询子记录前先按数据范围解析父客户，不能仅凭 customer_id 直接读取子表。
	if s.profile == nil {
		return nil, ErrProfileUnavailable
	}
	customer, err := s.profile.FindByID(ctx, principal, customerID, false)
	if err != nil {
		return nil, err
	}
	models, err := s.profile.ListStakeholders(ctx, principal.TenantID, customerID)
	if err != nil {
		return nil, err
	}
	return &StakeholderCollectionResponse{CustomerVersion: customer.Version, Items: stakeholderResponses(models)}, nil
}

func (s *Service) ReplaceStakeholders(ctx context.Context, customerID uint64, input ReplaceStakeholdersRequest) (*StakeholderCollectionResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Version == 0 || input.Reason == "" || utf8.RuneCountInString(input.Reason) > 500 || len(input.Items) > maxStakeholders {
		return nil, ErrInvalidStakeholders
	}
	if err = validateStakeholderInputs(input.Items); err != nil {
		return nil, err
	}
	if s.profile == nil {
		return nil, ErrProfileUnavailable
	}
	// 干系人采用整组替换而非逐条补丁：先锁定父客户并核对版本，再重建子项、提升父版本、
	// 写变更日志和审计。任一步失败都会回滚，防止子项集合与客户版本分离。
	var response StakeholderCollectionResponse
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		customer, lockErr := s.profile.LockActiveCustomerForProfile(txCtx, principal, customerID)
		if lockErr != nil {
			return lockErr
		}
		if customer.Version != input.Version {
			return ErrVersionConflict
		}
		existing, listErr := s.profile.ListStakeholders(txCtx, principal.TenantID, customerID)
		if listErr != nil {
			return listErr
		}
		models, buildErr := s.buildStakeholders(principal.TenantID, principal.UserID, customerID, input.Items, existing)
		if buildErr != nil {
			return buildErr
		}
		before := stakeholderResponses(existing)
		if replaceErr := s.profile.ReplaceStakeholders(txCtx, principal.TenantID, customerID, principal.UserID, models); replaceErr != nil {
			return replaceErr
		}
		if versionErr := s.profile.IncrementProfileVersion(txCtx, principal.TenantID, customerID, input.Version, principal.UserID); versionErr != nil {
			return versionErr
		}
		after := stakeholderResponses(models)
		if logErr := s.profile.CreateChangeLog(txCtx, &ChangeLog{TenantID: principal.TenantID, CustomerID: customerID, FieldName: "stakeholders", BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(after), Reason: input.Reason, OperatorID: principal.UserID, RequestID: requestctx.ID(txCtx), OccurredAt: s.now()}); logErr != nil {
			return logErr
		}
		response = StakeholderCollectionResponse{CustomerVersion: input.Version + 1, Items: after}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "STAKEHOLDERS_REPLACE", ResourceType: "customer", ResourceID: stringUint(customerID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(after), Reason: input.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	return &response, nil
}

func (s *Service) buildStakeholders(tenantID, actorID string, customerID uint64, inputs []StakeholderInput, existing []Stakeholder) ([]Stakeholder, error) {
	// 可选敏感字段区分“未提交”和“显式提交”：nil 沿用旧密文与掩码，非 nil 才重新加密；
	// 空字符串表示明确清空。客户端不能把掩码文本回传成新的联系方式。
	byID := make(map[uint64]Stakeholder, len(existing))
	for _, item := range existing {
		byID[item.ID] = item
	}
	seen := make(map[uint64]struct{}, len(inputs))
	models := make([]Stakeholder, 0, len(inputs))
	for index, item := range inputs {
		var prior Stakeholder
		if item.ID != 0 {
			var ok bool
			prior, ok = byID[item.ID]
			if !ok {
				return nil, ErrInvalidStakeholders
			}
			if _, duplicate := seen[item.ID]; duplicate {
				return nil, ErrInvalidStakeholders
			}
			seen[item.ID] = struct{}{}
		}
		phoneCipher, phoneMasked := prior.PhoneCipher, prior.PhoneMasked
		if item.Phone != nil {
			phone := strings.TrimSpace(*item.Phone)
			var encryptErr error
			phoneCipher, encryptErr = s.codec.Encrypt(phone)
			if encryptErr != nil {
				return nil, encryptErr
			}
			phoneMasked = ""
			if phone != "" {
				phoneMasked = maskStakeholderPhone(phone)
			}
		}
		emailCipher, emailMasked := prior.EmailCipher, prior.EmailMasked
		if item.Email != nil {
			email := strings.TrimSpace(*item.Email)
			var encryptErr error
			emailCipher, encryptErr = s.codec.Encrypt(email)
			if encryptErr != nil {
				return nil, encryptErr
			}
			emailMasked = maskEmail(email)
		}
		models = append(models, Stakeholder{
			Model: database.Model{TenantID: tenantID, CreatedBy: actorID, UpdatedBy: actorID, Version: 1}, CustomerID: customerID,
			Name: strings.TrimSpace(item.Name), RoleTitle: strings.TrimSpace(item.RoleTitle), Influence: strings.ToUpper(strings.TrimSpace(item.Influence)),
			RelationshipSummary: strings.TrimSpace(item.RelationshipSummary), PhoneCipher: phoneCipher, PhoneMasked: phoneMasked,
			EmailCipher: emailCipher, EmailMasked: emailMasked, SortOrder: index,
		})
	}
	return models, nil
}

func (s *Service) ListInformationSystems(ctx context.Context, customerID uint64) (*InformationSystemCollectionResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.profile == nil {
		return nil, ErrProfileUnavailable
	}
	customer, err := s.profile.FindByID(ctx, principal, customerID, false)
	if err != nil {
		return nil, err
	}
	models, err := s.profile.ListInformationSystems(ctx, principal.TenantID, customerID)
	if err != nil {
		return nil, err
	}
	return &InformationSystemCollectionResponse{CustomerVersion: customer.Version, Items: informationSystemResponses(models)}, nil
}

func (s *Service) ReplaceInformationSystems(ctx context.Context, customerID uint64, input ReplaceInformationSystemsRequest) (*InformationSystemCollectionResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.profile == nil {
		return nil, ErrProfileUnavailable
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Version == 0 || input.Reason == "" || utf8.RuneCountInString(input.Reason) > 500 || len(input.Items) > maxInformationSystems {
		return nil, ErrInvalidSystems
	}
	// 信息系统同样按有序集合整体替换；父客户行锁和乐观版本阻止两个编辑者用旧快照互相覆盖。
	models, err := buildInformationSystems(principal.TenantID, principal.UserID, customerID, input.Items)
	if err != nil {
		return nil, err
	}
	var response InformationSystemCollectionResponse
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		customer, lockErr := s.profile.LockActiveCustomerForProfile(txCtx, principal, customerID)
		if lockErr != nil {
			return lockErr
		}
		if customer.Version != input.Version {
			return ErrVersionConflict
		}
		existing, listErr := s.profile.ListInformationSystems(txCtx, principal.TenantID, customerID)
		if listErr != nil {
			return listErr
		}
		before := informationSystemResponses(existing)
		if replaceErr := s.profile.ReplaceInformationSystems(txCtx, principal.TenantID, customerID, principal.UserID, models); replaceErr != nil {
			return replaceErr
		}
		if versionErr := s.profile.IncrementProfileVersion(txCtx, principal.TenantID, customerID, input.Version, principal.UserID); versionErr != nil {
			return versionErr
		}
		after := informationSystemResponses(models)
		if logErr := s.profile.CreateChangeLog(txCtx, &ChangeLog{TenantID: principal.TenantID, CustomerID: customerID, FieldName: "systems", BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(after), Reason: input.Reason, OperatorID: principal.UserID, RequestID: requestctx.ID(txCtx), OccurredAt: s.now()}); logErr != nil {
			return logErr
		}
		response = InformationSystemCollectionResponse{CustomerVersion: input.Version + 1, Items: after}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "SYSTEMS_REPLACE", ResourceType: "customer", ResourceID: stringUint(customerID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before), AfterJSON: audit.JSON(after), Reason: input.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	return &response, nil
}

func validateStakeholderInputs(items []StakeholderInput) error {
	// 掩码只用于展示，不是可写格式；包含星号的联系方式必须由用户重新输入完整值，
	// 否则可能把掩码永久加密保存并丢失原始联系方式。
	for _, item := range items {
		name, role := strings.TrimSpace(item.Name), strings.TrimSpace(item.RoleTitle)
		influence := strings.ToUpper(strings.TrimSpace(item.Influence))
		if name == "" || role == "" || utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(role) > 100 || utf8.RuneCountInString(strings.TrimSpace(item.RelationshipSummary)) > 500 || unsafeText(name) || unsafeText(role) || unsafeText(item.RelationshipSummary) {
			return ErrInvalidStakeholders
		}
		if _, ok := stakeholderInfluences[influence]; !ok {
			return ErrInvalidStakeholders
		}
		if item.Phone != nil {
			phone := strings.TrimSpace(*item.Phone)
			if strings.Contains(phone, "*") || !validPhone(phone) {
				return ErrInvalidStakeholders
			}
		}
		if item.Email != nil {
			email := strings.TrimSpace(*item.Email)
			if strings.Contains(email, "*") || !validEmail(email) {
				return ErrInvalidStakeholders
			}
		}
	}
	return nil
}

func buildInformationSystems(tenantID, actorID string, customerID uint64, inputs []InformationSystemInput) ([]InformationSystem, error) {
	// 等保级别和备案状态使用封闭枚举，测评日期必须是规范 YYYY-MM-DD；SortOrder 直接取
	// 请求数组顺序，使整组替换后展示顺序具有确定性。
	models := make([]InformationSystem, 0, len(inputs))
	for index, item := range inputs {
		name := strings.TrimSpace(item.Name)
		level := strings.ToUpper(strings.TrimSpace(item.ProtectionLevel))
		status := strings.ToUpper(strings.TrimSpace(item.FilingStatus))
		filingNo := strings.TrimSpace(item.FilingNo)
		scenario := strings.TrimSpace(item.ApplicationScenario)
		if name == "" || utf8.RuneCountInString(name) > 200 || utf8.RuneCountInString(scenario) > 500 || utf8.RuneCountInString(filingNo) > 100 || unsafeText(name) || unsafeText(scenario) || unsafeText(filingNo) {
			return nil, ErrInvalidSystems
		}
		if _, ok := systemProtectionLevels[level]; !ok {
			return nil, ErrInvalidSystems
		}
		if _, ok := systemFilingStatuses[status]; !ok {
			return nil, ErrInvalidSystems
		}
		var gradingDate *time.Time
		if item.GradingDate != nil {
			value := strings.TrimSpace(*item.GradingDate)
			if value != "" {
				parsed, parseErr := time.Parse("2006-01-02", value)
				if parseErr != nil || parsed.Format("2006-01-02") != value || parsed.Year() < 1000 {
					return nil, ErrInvalidSystems
				}
				gradingDate = &parsed
			}
		}
		models = append(models, InformationSystem{Model: database.Model{TenantID: tenantID, CreatedBy: actorID, UpdatedBy: actorID, Version: 1}, CustomerID: customerID, Name: name, ProtectionLevel: level, ApplicationScenario: scenario, FilingNo: filingNo, GradingDate: gradingDate, FilingStatus: status, SortOrder: index})
	}
	return models, nil
}

func stakeholderResponses(models []Stakeholder) []StakeholderResponse {
	items := make([]StakeholderResponse, 0, len(models))
	for _, model := range models {
		items = append(items, StakeholderResponse{ID: model.ID, Name: model.Name, RoleTitle: model.RoleTitle, Influence: model.Influence, RelationshipSummary: model.RelationshipSummary, PhoneMasked: model.PhoneMasked, EmailMasked: model.EmailMasked, SortOrder: model.SortOrder, Version: model.Version})
	}
	return items
}

func informationSystemResponses(models []InformationSystem) []InformationSystemResponse {
	items := make([]InformationSystemResponse, 0, len(models))
	for _, model := range models {
		var gradingDate *string
		if model.GradingDate != nil {
			value := model.GradingDate.Format("2006-01-02")
			gradingDate = &value
		}
		items = append(items, InformationSystemResponse{ID: model.ID, Name: model.Name, ProtectionLevel: model.ProtectionLevel, ApplicationScenario: model.ApplicationScenario, FilingNo: model.FilingNo, GradingDate: gradingDate, FilingStatus: model.FilingStatus, SortOrder: model.SortOrder, Version: model.Version})
	}
	return items
}

func validPhone(value string) bool {
	if value == "" {
		return true
	}
	if utf8.RuneCountInString(value) > 32 {
		return false
	}
	digits := 0
	for _, char := range value {
		if unicode.IsDigit(char) {
			digits++
			continue
		}
		if !strings.ContainsRune("+-() ", char) {
			return false
		}
	}
	return digits >= 7
}

func validEmail(value string) bool {
	if value == "" {
		return true
	}
	if utf8.RuneCountInString(value) > 200 {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

func unsafeText(value string) bool {
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

func maskStakeholderPhone(value string) string {
	// 常见手机号沿用统一脱敏格式；短号或分机不能借另一种掩码暴露额外格式信息。
	return security.MaskPhone(value)
}
