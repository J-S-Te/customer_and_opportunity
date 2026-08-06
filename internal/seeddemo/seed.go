package seeddemo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/gorm"
)

// 注入生产服务所需依赖，使演示数据沿用真实加密、审计、编号和幂等记录规则，而不是绕过领域层写表。
type Dependencies struct {
	DB        *gorm.DB
	Codec     *security.SensitiveCodec
	TenantID  string
	ActorID   string
	ActorName string
	// Owners 可选：把演示人员（按姓名）映射到平台真实用户，避免负责人显示为占位 ID。
	Owners map[string]Person
	Now    time.Time
}

type CustomerResult struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	CustomerNo string `json:"customer_no"`
	ID         uint64 `json:"id"`
	Created    bool   `json:"created"`
}

type OpportunityResult struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	OpportunityNo string `json:"opportunity_no"`
	ID            uint64 `json:"id"`
	Stage         string `json:"stage"`
	Created       bool   `json:"created"`
}

type Summary struct {
	Customers            []CustomerResult
	Opportunities        []OpportunityResult
	CustomersCreated     int
	CustomersSkipped     int
	OpportunitiesCreated int
	OpportunitiesSkipped int
}

type seeder struct {
	db              *gorm.DB
	codec           *security.SensitiveCodec
	tenantID        string
	actorID         string
	actorName       string
	owners          map[string]Person
	now             time.Time
	audit           audit.Writer
	customer        *customer.Service
	opportunity     *opportunity.Service
	opportunityRepo *opportunity.GORMRepository
	principal       auth.Principal
}

// 幂等写入演示数据：客户按租户和规范化名称匹配，商机按租户、客户和名称匹配；重复运行不会
// 创建副本，也不会重新触发阶段流转。
func Run(ctx context.Context, deps Dependencies) (Summary, error) {
	if deps.DB == nil {
		return Summary{}, errors.New("seed database must not be nil")
	}
	if deps.Codec == nil {
		return Summary{}, errors.New("sensitive codec must not be nil")
	}
	tenantID := strings.TrimSpace(deps.TenantID)
	actorID := strings.TrimSpace(deps.ActorID)
	actorName := strings.TrimSpace(deps.ActorName)
	if tenantID == "" || actorID == "" || actorName == "" {
		return Summary{}, errors.New("tenant id, actor id and actor name are required")
	}
	now := deps.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	auditWriter := audit.NewGORMWriter(deps.DB)
	seed := &seeder{
		db: deps.DB, codec: deps.Codec, tenantID: tenantID, actorID: actorID, actorName: actorName, owners: deps.Owners, now: now,
		audit:           auditWriter,
		customer:        customer.NewService(deps.DB, customer.NewGORMRepository(deps.DB), auditWriter, deps.Codec),
		opportunityRepo: opportunity.NewGORMRepository(deps.DB),
		principal: auth.Principal{
			TenantID: tenantID, UserID: actorID, DisplayName: actorName,
			Permissions: map[string]struct{}{"customer.create": {}}, ScopeMode: auth.ScopeAll,
		},
	}
	seed.opportunity = opportunity.NewService(deps.DB, seed.opportunityRepo, auditWriter, nil)

	var summary Summary
	customerIDs := make(map[string]uint64, 4)
	for _, item := range customers() {
		result, err := seed.seedCustomer(ctx, item)
		if err != nil {
			return Summary{}, fmt.Errorf("seed customer %s: %w", item.Key, err)
		}
		customerIDs[item.Key] = result.ID
		summary.Customers = append(summary.Customers, result)
		if result.Created {
			summary.CustomersCreated++
		} else {
			summary.CustomersSkipped++
		}
	}
	for _, item := range opportunities() {
		customerID, ok := customerIDs[item.CustomerKey]
		if !ok {
			return Summary{}, fmt.Errorf("opportunity %s references unknown customer %s", item.Key, item.CustomerKey)
		}
		result, err := seed.seedOpportunity(ctx, item, customerID)
		if err != nil {
			return Summary{}, fmt.Errorf("seed opportunity %s: %w", item.Key, err)
		}
		summary.Opportunities = append(summary.Opportunities, result)
		if result.Created {
			summary.OpportunitiesCreated++
		} else {
			summary.OpportunitiesSkipped++
		}
	}
	return summary, nil
}

// resolveOwner 优先使用显式真实用户覆盖；未覆盖时回退到当前 actor（真实用户），
// 不再把原型占位 subject 当作负责人写入业务数据。
func (seed *seeder) resolveOwner(key string) Person {
	if override, ok := seed.owners[key]; ok && strings.TrimSpace(override.Sub) != "" {
		return override
	}
	return Person{Sub: seed.actorID, Name: seed.actorName}
}

func (seed *seeder) seedCustomer(ctx context.Context, item customerSeed) (CustomerResult, error) {
	// 先按业务唯一语义查找，再使用稳定幂等键调用正式创建服务；并发执行时最终仍由服务端
	// 幂等记录和数据库约束收敛。
	var existing customer.Customer
	err := seed.db.WithContext(ctx).
		Where("tenant_id = ? AND normalized_name = ? AND deleted_at IS NULL", seed.tenantID, normalizeName(item.Name)).
		First(&existing).Error
	if err == nil {
		return CustomerResult{Key: item.Key, Name: existing.Name, CustomerNo: existing.CustomerNo, ID: existing.ID}, nil
	}
	if err != gorm.ErrRecordNotFound {
		return CustomerResult{}, err
	}

	owner := seed.resolveOwner(item.OwnerKey)
	contacts := make([]customer.ContactInput, 0, len(item.Contacts))
	for _, contact := range item.Contacts {
		contacts = append(contacts, customer.ContactInput{
			Name: contact.Name, Phone: contact.Phone, Email: contact.Email, IsRegistration: contact.Registration,
		})
	}
	created, err := seed.customer.Create(seed.actorCtx(ctx), customer.CreateRequest{
		Name: item.Name, UnifiedCreditCode: item.UnifiedCreditCode, CustomerType: item.CustomerType,
		Industry: item.Industry, Region: item.Region, OwnerUserID: owner.Sub, OwnerOrgID: owner.OrgID,
		Contacts: contacts, Reason: "演示数据初始化", IdempotencyKey: "seed-demo-customer-" + item.Key,
	})
	if err != nil {
		return CustomerResult{}, err
	}
	if err := seed.seedCustomerProfile(ctx, created.ID, item); err != nil {
		return CustomerResult{}, err
	}
	return CustomerResult{Key: item.Key, Name: created.Name, CustomerNo: created.CustomerNo, ID: created.ID, Created: true}, nil
}

func (seed *seeder) seedCustomerProfile(ctx context.Context, customerID uint64, item customerSeed) error {
	// 子档案通过正式 Replace 接口写入，确保审计字段、租户条件和敏感字段加密与线上编辑一致。
	repo := customer.NewGORMRepository(seed.db)
	if len(item.Stakeholders) > 0 {
		models := make([]customer.Stakeholder, 0, len(item.Stakeholders))
		for index, stakeholder := range item.Stakeholders {
			phoneCipher, phoneErr := seed.codec.Encrypt(stakeholder.Phone)
			if phoneErr != nil {
				return phoneErr
			}
			emailCipher, emailErr := seed.codec.Encrypt(stakeholder.Email)
			if emailErr != nil {
				return emailErr
			}
			model := customer.Stakeholder{
				CustomerID: customerID, Name: stakeholder.Name, RoleTitle: stakeholder.RoleTitle,
				Influence: strings.ToUpper(stakeholder.Influence), RelationshipSummary: stakeholder.RelationshipSummary,
				PhoneCipher: phoneCipher, PhoneMasked: security.MaskPhone(stakeholder.Phone),
				EmailCipher: emailCipher, EmailMasked: maskEmail(stakeholder.Email), SortOrder: index + 1,
			}
			seed.auditColumns(&model.CreatedBy, &model.UpdatedBy, &model.CreatedAt, &model.UpdatedAt)
			model.TenantID, model.Version = seed.tenantID, 1
			models = append(models, model)
		}
		if err := repo.ReplaceStakeholders(ctx, seed.tenantID, customerID, seed.actorID, models); err != nil {
			return err
		}
	}
	if len(item.Systems) > 0 {
		models := make([]customer.InformationSystem, 0, len(item.Systems))
		for index, system := range item.Systems {
			gradingDate, parseErr := time.Parse("2006-01-02", system.GradingDate)
			if parseErr != nil {
				return parseErr
			}
			model := customer.InformationSystem{
				CustomerID: customerID, Name: system.Name, ProtectionLevel: system.ProtectionLevel,
				ApplicationScenario: system.ApplicationScenario, FilingNo: system.FilingNo,
				FilingStatus: system.FilingStatus, GradingDate: &gradingDate, SortOrder: index + 1,
			}
			seed.auditColumns(&model.CreatedBy, &model.UpdatedBy, &model.CreatedAt, &model.UpdatedAt)
			model.TenantID, model.Version = seed.tenantID, 1
			models = append(models, model)
		}
		if err := repo.ReplaceInformationSystems(ctx, seed.tenantID, customerID, seed.actorID, models); err != nil {
			return err
		}
	}
	for _, followup := range item.Followups {
		followedAt, parseErr := time.Parse(time.RFC3339, followup.FollowedAt)
		if parseErr != nil {
			return parseErr
		}
		model := &customer.Followup{
			CustomerID: customerID, Type: followup.Type, Content: followup.Content,
			FollowedAt: followedAt.UTC(), FollowedBy: seed.actorID,
		}
		if followup.NextFollowAt != "" {
			next, nextErr := time.Parse(time.RFC3339, followup.NextFollowAt)
			if nextErr != nil {
				return nextErr
			}
			model.NextFollowAt = &next
		}
		model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = seed.tenantID, seed.actorID, seed.actorID, 1
		model.CreatedAt, model.UpdatedAt = seed.now, seed.now
		if err := repo.CreateFollowup(ctx, model); err != nil {
			return err
		}
	}
	return nil
}

func (seed *seeder) seedOpportunity(ctx context.Context, item opportunitySeed, customerID uint64) (OpportunityResult, error) {
	var existing opportunity.Opportunity
	err := seed.db.WithContext(ctx).
		Where("tenant_id = ? AND customer_id = ? AND name = ? AND deleted_at IS NULL", seed.tenantID, customerID, item.Name).
		First(&existing).Error
	if err == nil {
		return OpportunityResult{Key: item.Key, Name: existing.Name, OpportunityNo: existing.OpportunityNo, ID: existing.ID, Stage: existing.CurrentStage}, nil
	}
	if err != gorm.ErrRecordNotFound {
		return OpportunityResult{}, err
	}

	owner := seed.resolveOwner(item.OwnerKey)
	input := opportunity.CreateRequest{
		Name: item.Name, CustomerID: customerID, Type: item.Type, Source: item.Source,
		ExpectedAmount: item.ExpectedAmount, ExpectedSignDate: item.ExpectedSignDate,
		RequirementSummary: item.RequirementSummary, SystemCount: item.SystemCount,
		PainPoints: item.PainPoints, CompetitorInfo: item.CompetitorInfo,
		OwnerUserID: owner.Sub, OwnerOrgID: owner.OrgID, IdempotencyKey: "seed-demo-opportunity-" + item.Key,
	}

	var result OpportunityResult
	if item.Terminal == nil {
		created, createErr := seed.opportunity.Create(seed.actorCtx(ctx), input)
		if createErr != nil {
			return OpportunityResult{}, createErr
		}
		result = OpportunityResult{Key: item.Key, Name: created.Name, OpportunityNo: created.OpportunityNo, ID: created.ID, Stage: created.CurrentStage, Created: true}
		if item.Stage != "初步接触" && item.Stage != "" {
			stageCtx := requestctx.WithID(seed.actorCtx(ctx), "seed-demo-stage-"+item.Key)
			stageInput := opportunity.StageChangeRequest{TargetStage: item.Stage, Reason: "演示数据阶段推进", Version: created.Version}
			if _, stageErr := seed.opportunity.ChangeStage(stageCtx, created.ID, stageInput); stageErr != nil {
				return OpportunityResult{}, stageErr
			}
			result.Stage = item.Stage
		}
	} else {
		model, terminalErr := seed.createTerminalOpportunity(ctx, item.Key, input, *item.Terminal)
		if terminalErr != nil {
			return OpportunityResult{}, terminalErr
		}
		result = OpportunityResult{Key: item.Key, Name: model.Name, OpportunityNo: model.OpportunityNo, ID: model.ID, Stage: model.CurrentStage, Created: true}
	}

	model, findErr := seed.opportunityRepo.FindByID(ctx, seed.principal, result.ID)
	if findErr != nil {
		return OpportunityResult{}, findErr
	}
	if len(item.Members) > 0 {
		desired := make([]opportunity.Member, 0, len(item.Members))
		for _, member := range item.Members {
			person := People()[member.UserKey]
			desired = append(desired, opportunity.Member{
				Model: database.Model{
					TenantID: seed.tenantID, CreatedBy: seed.actorID, UpdatedBy: seed.actorID,
					CreatedAt: seed.now, UpdatedAt: seed.now, Version: 1,
				},
				OpportunityID: result.ID, UserID: person.Sub, Role: member.Role, IsActive: true,
			})
		}
		if replaceErr := seed.opportunityRepo.ReplaceMembers(ctx, model, model.Version, desired, seed.now); replaceErr != nil {
			return OpportunityResult{}, replaceErr
		}
	}
	for _, followup := range item.Followups {
		followedAt, parseErr := time.Parse(time.RFC3339, followup.FollowedAt)
		if parseErr != nil {
			return OpportunityResult{}, parseErr
		}
		followupModel := &opportunity.Followup{
			OpportunityID: result.ID, Type: followup.Type, Content: followup.Content,
			FollowedAt: followedAt.UTC(), FollowedBy: seed.actorID,
		}
		if followup.NextFollowAt != "" {
			next, nextErr := time.Parse(time.RFC3339, followup.NextFollowAt)
			if nextErr != nil {
				return OpportunityResult{}, nextErr
			}
			followupModel.NextFollowAt = &next
		}
		followupModel.TenantID, followupModel.CreatedBy, followupModel.UpdatedBy, followupModel.Version = seed.tenantID, seed.actorID, seed.actorID, 1
		followupModel.CreatedAt, followupModel.UpdatedAt = seed.now, seed.now
		if createErr := seed.opportunityRepo.CreateFollowup(ctx, followupModel); createErr != nil {
			return OpportunityResult{}, createErr
		}
	}
	return result, nil
}

// 终态商机直接构造，是因为签约/失败流转依赖合同提供方或失败原因，种子任务不应伪造外部服务；
// 主记录、阶段日志和审计事件仍在同一事务提交。
func (seed *seeder) createTerminalOpportunity(ctx context.Context, key string, input opportunity.CreateRequest, terminal terminalSeed) (*opportunity.Opportunity, error) {
	amount, err := decimal.NewFromString(input.ExpectedAmount)
	if err != nil || !amount.IsPositive() {
		return nil, errors.New("expected amount must be a positive decimal")
	}
	signDate, err := time.Parse("2006-01-02", input.ExpectedSignDate)
	if err != nil {
		return nil, err
	}
	number, err := seed.opportunityRepo.NextNumber(ctx, seed.tenantID, seed.now.Format("20060102"))
	if err != nil {
		return nil, err
	}
	var contractRef, lostReason *string
	if terminal.ContractRef != "" {
		value := terminal.ContractRef
		contractRef = &value
	}
	if terminal.LostReason != "" {
		value := terminal.LostReason
		lostReason = &value
	}
	model := &opportunity.Opportunity{
		OpportunityNo: number, Name: input.Name, CustomerID: input.CustomerID, Type: input.Type, Source: input.Source,
		ExpectedAmount: amount, ExpectedSignDate: signDate, RequirementSummary: input.RequirementSummary,
		SystemCount: input.SystemCount, PainPoints: input.PainPoints, CompetitorInfo: input.CompetitorInfo,
		OwnerUserID: input.OwnerUserID, OwnerOrgID: input.OwnerOrgID,
		CurrentStage: terminal.Stage, Status: opportunity.StatusClosed, ContractRef: contractRef,
		LostReason: lostReason, TerminalPendingType: terminal.PendingType, StageChangedAt: seed.now,
	}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = seed.tenantID, seed.actorID, seed.actorID, 1
	model.CreatedAt, model.UpdatedAt = seed.now, seed.now
	stageLog := &opportunity.StageLog{
		TenantID: seed.tenantID, OpportunityID: model.ID, FromStage: terminal.FromStage, ToStage: terminal.Stage,
		Source: opportunity.SourceManual, SourceID: "seed-demo-terminal-" + key,
		Reason: "演示数据终态初始化", ContractRef: contractRef, LostReason: lostReason,
		PendingType: terminal.PendingType, OperatorID: seed.actorID, ChangedAt: seed.now,
		RequestID: "seed-demo-terminal-" + key + "-stage",
	}
	err = database.WithTransaction(ctx, seed.db, func(txCtx context.Context) error {
		if createErr := seed.opportunityRepo.Create(txCtx, model); createErr != nil {
			return createErr
		}
		if logErr := seed.opportunityRepo.CreateStageLog(txCtx, stageLog); logErr != nil {
			return logErr
		}
		after := audit.JSON(opportunityResponse(model))
		if auditErr := seed.audit.Write(txCtx, audit.Event{
			TenantID: seed.tenantID, Module: "opportunity", Operation: "CREATE", ResourceType: "opportunity",
			ResourceID: uintString(model.ID), ActorID: seed.actorID, ActorNameSnapshot: seed.actorName,
			AfterJSON: after, Reason: "演示数据初始化", Result: "SUCCESS",
		}); auditErr != nil {
			return auditErr
		}
		return seed.audit.Write(txCtx, audit.Event{
			TenantID: seed.tenantID, Module: "opportunity", Operation: "STAGE_CHANGE", ResourceType: "opportunity",
			ResourceID: uintString(model.ID), ActorID: seed.actorID, ActorNameSnapshot: seed.actorName,
			AfterJSON: after, Reason: "演示数据终态初始化", Result: "SUCCESS",
		})
	})
	if err != nil {
		return nil, err
	}
	return model, nil
}

func (seed *seeder) auditColumns(createdBy, updatedBy *string, createdAt, updatedAt *time.Time) {
	*createdBy, *updatedBy, *createdAt, *updatedAt = seed.actorID, seed.actorID, seed.now, seed.now
}

func (seed *seeder) actorCtx(ctx context.Context) context.Context {
	return auth.WithPrincipal(ctx, seed.principal)
}

func opportunityResponse(model *opportunity.Opportunity) opportunity.Response {
	return opportunity.Response{
		ID: model.ID, OpportunityNo: model.OpportunityNo, Name: model.Name, CustomerID: model.CustomerID,
		Type: model.Type, Source: model.Source, ExpectedAmount: model.ExpectedAmount.StringFixed(2),
		ExpectedSignDate: model.ExpectedSignDate.Format("2006-01-02"), RequirementSummary: model.RequirementSummary,
		SystemCount: model.SystemCount, PainPoints: model.PainPoints, CompetitorInfo: model.CompetitorInfo,
		OwnerUserID: model.OwnerUserID, OwnerOrgID: model.OwnerOrgID, CurrentStage: model.CurrentStage,
		Status: model.Status, ContractRef: model.ContractRef, LostReason: model.LostReason,
		TerminalPendingType: model.TerminalPendingType, StageChangedAt: model.StageChangedAt,
		Version: model.Version, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

func normalizeName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), ""))
}

func maskEmail(value string) string {
	at := strings.IndexByte(value, '@')
	if at <= 0 {
		return strings.TrimSpace(value)
	}
	return value[:1] + "***" + value[at:]
}

func uintString(value uint64) string {
	return fmt.Sprintf("%d", value)
}
