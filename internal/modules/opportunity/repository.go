package opportunity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository interface {
	NextNumber(context.Context, string, string) (string, error)
	CustomerVisible(context.Context, auth.Principal, uint64) (bool, error)
	Create(context.Context, *Opportunity) error
	FindCreatedByID(context.Context, string, uint64) (*Opportunity, error)
	FindCreateIdempotency(context.Context, string, string, string) (*CreateIdempotency, error)
	CreateCreateIdempotency(context.Context, *CreateIdempotency) error
	FindByID(context.Context, auth.Principal, uint64) (*Opportunity, error)
	FindByIDForUpdate(context.Context, auth.Principal, uint64) (*Opportunity, error)
	List(context.Context, auth.Principal, ListQuery) (pagination.Page[Response], error)
	Update(context.Context, *Opportunity, uint64) error
	UpdateLifecycle(context.Context, *Opportunity, uint64, string) error
	VoidBlockers(context.Context, string, uint64) ([]string, error)
	UpdateStage(context.Context, *Opportunity, uint64) error
	CreateStageLog(context.Context, *StageLog) error
	CreateExternalLink(context.Context, *ExternalLink) error
	FindExternalLink(context.Context, string, uint64, string, string) (*ExternalLink, error)
	LatestExternalLink(context.Context, string, uint64) (*ExternalLink, error)
	LatestApprovedQuotation(context.Context, string, uint64) (*ExternalLink, error)
	Board(context.Context, auth.Principal, ListQuery) ([]Response, error)
	StageHistory(context.Context, string, uint64, int, int) (pagination.Page[StageHistoryResponse], error)
	CreateFollowup(context.Context, *Followup) error
	ListFollowups(context.Context, string, uint64, int, int) (pagination.Page[FollowupResponse], error)
	UpdateTerminalTodo(context.Context, *Opportunity, uint64) error
	ListMembers(context.Context, string, uint64, bool) ([]Member, error)
	ListMemberTerms(context.Context, string, uint64, MemberTermQuery) (pagination.Page[MemberTermResponse], error)
	UpdateOwner(context.Context, *Opportunity, uint64) error
	ReplaceMembers(context.Context, *Opportunity, uint64, []Member, time.Time) error
	CreateOutboxEvents(context.Context, []OutboxEvent) error
	FindOutboxEvent(context.Context, string, string) (*OutboxEvent, error)
	FindChangeIdempotency(context.Context, string, uint64, string, string, string) (*ChangeIdempotency, error)
	FindChangeIdempotencyForUpdate(context.Context, string, uint64, string, string, string) (*ChangeIdempotency, error)
	CreateChangeIdempotency(context.Context, *ChangeIdempotency) error
}

func (r *GORMRepository) Board(ctx context.Context, principal auth.Principal, query ListQuery) ([]Response, error) {
	db := scoped(database.FromContext(ctx, r.db).Model(&Opportunity{}), principal).Where("opp_status <> ?", "VOID")
	if query.Keyword != "" {
		db = db.Where("opportunity_no LIKE ? OR name LIKE ?", "%"+query.Keyword+"%", "%"+query.Keyword+"%")
	}
	if query.OwnerID != "" {
		db = db.Where("owner_user_id = ?", query.OwnerID)
	}
	var models []Opportunity
	if err := db.Order("stage_changed_at DESC").Order("id DESC").Limit(1000).Find(&models).Error; err != nil {
		return nil, err
	}
	items := make([]Response, 0, len(models))
	for i := range models {
		items = append(items, toResponse(&models[i]))
	}
	return items, nil
}

func (r *GORMRepository) StageHistory(ctx context.Context, tenantID string, opportunityID uint64, page, pageSize int) (pagination.Page[StageHistoryResponse], error) {
	page, pageSize = pagination.Normalize(page, pageSize)
	db := database.FromContext(ctx, r.db).Model(&StageLog{}).Where("tenant_id = ? AND opportunity_id = ?", tenantID, opportunityID)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[StageHistoryResponse]{}, err
	}
	var logs []StageLog
	if err := db.Order("changed_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error; err != nil {
		return pagination.Page[StageHistoryResponse]{}, err
	}
	items := make([]StageHistoryResponse, 0, len(logs))
	for _, log := range logs {
		items = append(items, StageHistoryResponse{ID: log.ID, FromStage: log.FromStage, ToStage: log.ToStage, Source: log.Source, Reason: log.Reason, ContractRef: log.ContractRef, LostReason: log.LostReason, PendingType: log.PendingType, OperatorID: log.OperatorID, ChangedAt: log.ChangedAt, RequestID: log.RequestID})
	}
	return pagination.Page[StageHistoryResponse]{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (r *GORMRepository) CreateFollowup(ctx context.Context, model *Followup) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) ListFollowups(ctx context.Context, tenantID string, opportunityID uint64, page, pageSize int) (pagination.Page[FollowupResponse], error) {
	page, pageSize = pagination.Normalize(page, pageSize)
	db := database.FromContext(ctx, r.db).Model(&Followup{}).Where("tenant_id = ? AND opportunity_id = ? AND deleted_at IS NULL", tenantID, opportunityID)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	var models []Followup
	if err := db.Order("followed_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&models).Error; err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	items := make([]FollowupResponse, 0, len(models))
	for _, model := range models {
		items = append(items, toFollowupResponse(&model))
	}
	return pagination.Page[FollowupResponse]{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (r *GORMRepository) UpdateTerminalTodo(ctx context.Context, model *Opportunity, expectedVersion uint64) error {
	result := database.FromContext(ctx, r.db).Model(&Opportunity{}).Where("id = ? AND tenant_id = ? AND version = ? AND deleted_at IS NULL", model.ID, model.TenantID, expectedVersion).Updates(map[string]any{"contract_ref": model.ContractRef, "lost_reason": model.LostReason, "terminal_pending_type": model.TerminalPendingType, "updated_by": model.UpdatedBy, "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	model.Version = expectedVersion + 1
	return nil
}

func toFollowupResponse(model *Followup) FollowupResponse {
	return FollowupResponse{ID: model.ID, Type: model.Type, Content: model.Content, FollowedAt: model.FollowedAt, FollowedBy: model.FollowedBy, NextFollowAt: model.NextFollowAt, CreatedAt: model.CreatedAt}
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }

func (r *GORMRepository) NextNumber(ctx context.Context, tenantID, date string) (string, error) {
	db := database.FromContext(ctx, r.db)
	result := db.Exec(`INSERT INTO crm_biz_sequences (tenant_id,business_date,business_type,current_value)
		VALUES (?,?,?,LAST_INSERT_ID(1)) ON DUPLICATE KEY UPDATE current_value=LAST_INSERT_ID(current_value+1)`, tenantID, date, "OPPORTUNITY")
	if result.Error != nil {
		return "", result.Error
	}
	var current uint64
	if err := db.Raw("SELECT LAST_INSERT_ID()").Scan(&current).Error; err != nil {
		return "", err
	}
	if current > 9999 {
		return "", fmt.Errorf("daily opportunity sequence exhausted")
	}
	return fmt.Sprintf("SJ%s%04d", date, current), nil
}

func (r *GORMRepository) CustomerVisible(ctx context.Context, principal auth.Principal, id uint64) (bool, error) {
	// A shared row lock serializes child creation with customer merge's update
	// lock. Callers must invoke this inside the same transaction as Create.
	query := database.FromContext(ctx, r.db).Table("crm_customers").Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND id=? AND status=? AND merged_into_id IS NULL AND deleted_at IS NULL", principal.TenantID, id, "ACTIVE")
	switch principal.ScopeMode {
	case auth.ScopeAll:
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return false, nil
		}
		query = query.Where("owner_org_id IN ?", principal.OrganizationIDs)
	default:
		query = query.Where("owner_user_id=?", principal.UserID)
	}
	var row struct{ ID uint64 }
	if err := query.Select("id").Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return row.ID != 0, nil
}

func (r *GORMRepository) Create(ctx context.Context, model *Opportunity) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) FindCreatedByID(ctx context.Context, tenantID string, id uint64) (*Opportunity, error) {
	var model Opportunity
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND id=? AND deleted_at IS NULL", tenantID, id).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &model, err
}

func (r *GORMRepository) FindCreateIdempotency(ctx context.Context, tenantID, actorID, key string) (*CreateIdempotency, error) {
	var model CreateIdempotency
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND actor_id=? AND idempotency_key=?", tenantID, actorID, key).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

func (r *GORMRepository) CreateCreateIdempotency(ctx context.Context, model *CreateIdempotency) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) ListMembers(ctx context.Context, tenantID string, opportunityID uint64, includeInactive bool) ([]Member, error) {
	query := database.FromContext(ctx, r.db).Model(&Member{}).
		Where("tenant_id=? AND opportunity_id=? AND deleted_at IS NULL", tenantID, opportunityID)
	if !includeInactive {
		query = query.Where("is_active=TRUE")
	}
	var models []Member
	if err := query.Order("is_active DESC").Order("role ASC").Order("user_id ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

func (r *GORMRepository) ListMemberTerms(ctx context.Context, tenantID string, opportunityID uint64, query MemberTermQuery) (pagination.Page[MemberTermResponse], error) {
	page, pageSize := pagination.Normalize(query.Page, query.PageSize)
	db := database.FromContext(ctx, r.db).Model(&MemberTerm{}).
		Where("tenant_id=? AND opportunity_id=?", tenantID, opportunityID)
	if query.UserID != "" {
		db = db.Where("user_id=?", query.UserID)
	}
	if query.ActiveOnly {
		db = db.Where("active_user_id IS NOT NULL")
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[MemberTermResponse]{}, err
	}
	var terms []MemberTerm
	if err := db.Order("COALESCE(started_at,snapshot_at) DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&terms).Error; err != nil {
		return pagination.Page[MemberTermResponse]{}, err
	}
	items := make([]MemberTermResponse, 0, len(terms))
	for _, term := range terms {
		items = append(items, memberTermResponse(term))
	}
	return pagination.Page[MemberTermResponse]{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (r *GORMRepository) UpdateOwner(ctx context.Context, model *Opportunity, expectedVersion uint64) error {
	result := database.FromContext(ctx, r.db).Model(&Opportunity{}).
		Where("id=? AND tenant_id=? AND version=? AND opp_status<>? AND deleted_at IS NULL", model.ID, model.TenantID, expectedVersion, StatusVoid).
		Updates(map[string]any{
			"owner_user_id": model.OwnerUserID,
			"owner_org_id":  model.OwnerOrgID,
			"updated_by":    model.UpdatedBy,
			"version":       gorm.Expr("version+1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	model.Version = expectedVersion + 1
	return nil
}

func (r *GORMRepository) ReplaceMembers(ctx context.Context, opportunity *Opportunity, expectedVersion uint64, desired []Member, now time.Time) error {
	db := database.FromContext(ctx, r.db)
	result := db.Model(&Opportunity{}).
		Where("id=? AND tenant_id=? AND version=? AND opp_status<>? AND deleted_at IS NULL", opportunity.ID, opportunity.TenantID, expectedVersion, StatusVoid).
		Updates(map[string]any{"updated_by": opportunity.UpdatedBy, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}

	var current []Member
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND opportunity_id=? AND deleted_at IS NULL", opportunity.TenantID, opportunity.ID).
		Find(&current).Error; err != nil {
		return err
	}
	desiredByUser := make(map[string]Member, len(desired))
	for _, member := range desired {
		desiredByUser[member.UserID] = member
	}
	for index := range current {
		member := &current[index]
		desiredMember, keep := desiredByUser[member.UserID]
		if keep {
			wasActive, roleChanged := member.IsActive, member.Role != desiredMember.Role
			if wasActive && roleChanged {
				if err := closeMemberTerm(db, opportunity.TenantID, opportunity.ID, member.UserID, opportunity.UpdatedBy, now); err != nil {
					return err
				}
			}
			if err := db.Model(&Member{}).Where("id=? AND tenant_id=?", member.ID, opportunity.TenantID).
				Updates(map[string]any{"role": desiredMember.Role, "is_active": true, "ended_at": nil, "updated_by": opportunity.UpdatedBy, "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
			if !wasActive || roleChanged {
				if err := createMemberTerm(db, *member, desiredMember.Role, opportunity.UpdatedBy, now); err != nil {
					return err
				}
			}
			delete(desiredByUser, member.UserID)
			continue
		}
		if member.IsActive {
			if err := closeMemberTerm(db, opportunity.TenantID, opportunity.ID, member.UserID, opportunity.UpdatedBy, now); err != nil {
				return err
			}
			if err := db.Model(&Member{}).Where("id=? AND tenant_id=?", member.ID, opportunity.TenantID).
				Updates(map[string]any{"is_active": false, "ended_at": now, "updated_by": opportunity.UpdatedBy, "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
	}
	for _, member := range desiredByUser {
		if err := db.Create(&member).Error; err != nil {
			return err
		}
		if err := createMemberTerm(db, member, member.Role, opportunity.UpdatedBy, now); err != nil {
			return err
		}
	}
	opportunity.Version = expectedVersion + 1
	return nil
}

func closeMemberTerm(db *gorm.DB, tenantID string, opportunityID uint64, userID, actorID string, endedAt time.Time) error {
	result := db.Model(&MemberTerm{}).
		Where("tenant_id=? AND opportunity_id=? AND active_user_id=?", tenantID, opportunityID, userID).
		Updates(map[string]any{"ended_at": endedAt, "ended_by": actorID})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("active opportunity member term cardinality is invalid for subject %q", userID)
	}
	return nil
}

func createMemberTerm(db *gorm.DB, member Member, role, actorID string, startedAt time.Time) error {
	actor := actorID
	return db.Create(&MemberTerm{
		TenantID: member.TenantID, OpportunityID: member.OpportunityID, MemberID: member.ID,
		UserID: member.UserID, Role: role, StartedAt: &startedAt, StartedBy: &actor,
		SourceKind: MemberTermSourceRecorded,
	}).Error
}

func memberTermResponse(term MemberTerm) MemberTermResponse {
	return MemberTermResponse{
		ID: term.ID, UserID: term.UserID, Role: term.Role, StartedAt: term.StartedAt,
		SnapshotAt: term.SnapshotAt, ActiveAtSnapshot: term.ActiveAtSnapshot,
		EndedAt: term.EndedAt, StartedBy: term.StartedBy, EndedBy: term.EndedBy, SourceKind: term.SourceKind,
	}
}

func (r *GORMRepository) CreateOutboxEvents(ctx context.Context, events []OutboxEvent) error {
	if len(events) == 0 {
		return nil
	}
	return database.FromContext(ctx, r.db).Create(&events).Error
}

func (r *GORMRepository) FindChangeIdempotency(ctx context.Context, tenantID string, opportunityID uint64, operation, actorID, key string) (*ChangeIdempotency, error) {
	var model ChangeIdempotency
	err := database.FromContext(ctx, r.db).Where("tenant_id=? AND opportunity_id=? AND operation=? AND actor_id=? AND idempotency_key=?", tenantID, opportunityID, operation, actorID, key).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

// FindChangeIdempotencyForUpdate is a current read. After waiting on the
// opportunity row under MySQL REPEATABLE READ, a second ordinary snapshot read
// may otherwise miss a concurrent winner that committed while this request
// was blocked.
func (r *GORMRepository) FindChangeIdempotencyForUpdate(ctx context.Context, tenantID string, opportunityID uint64, operation, actorID, key string) (*ChangeIdempotency, error) {
	var model ChangeIdempotency
	err := database.FromContext(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND opportunity_id=? AND operation=? AND actor_id=? AND idempotency_key=?", tenantID, opportunityID, operation, actorID, key).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

func (r *GORMRepository) CreateChangeIdempotency(ctx context.Context, model *ChangeIdempotency) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) Update(ctx context.Context, model *Opportunity, expectedVersion uint64) error {
	result := database.FromContext(ctx, r.db).Model(&Opportunity{}).
		Where("id=? AND tenant_id=? AND version=? AND opp_status<>? AND deleted_at IS NULL", model.ID, model.TenantID, expectedVersion, StatusVoid).
		Updates(map[string]any{
			"name": model.Name, "type": model.Type, "source": model.Source,
			"expected_amount": model.ExpectedAmount, "expected_sign_date": model.ExpectedSignDate,
			"requirement_summary": model.RequirementSummary, "system_count": model.SystemCount,
			"pain_points": model.PainPoints, "competitor_info": model.CompetitorInfo,
			"updated_by": model.UpdatedBy, "version": gorm.Expr("version+1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	model.Version = expectedVersion + 1
	return nil
}

// UpdateLifecycle atomically guards both the expected version and source state.
func (r *GORMRepository) UpdateLifecycle(ctx context.Context, model *Opportunity, expectedVersion uint64, expectedStatus string) error {
	result := database.FromContext(ctx, r.db).Model(&Opportunity{}).
		Where("id=? AND tenant_id=? AND version=? AND opp_status=? AND deleted_at IS NULL", model.ID, model.TenantID, expectedVersion, expectedStatus).
		Updates(map[string]any{
			"opp_status": model.Status, "end_date": model.EndDate, "status_before_void": model.StatusBeforeVoid,
			"updated_by": model.UpdatedBy, "version": gorm.Expr("version+1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	model.Version = expectedVersion + 1
	return nil
}

func (r *GORMRepository) VoidBlockers(ctx context.Context, tenantID string, opportunityID uint64) ([]string, error) {
	db := database.FromContext(ctx, r.db)
	blockers := make([]string, 0, 2)
	var activePresale int64
	if err := db.Table("crm_presale_requests").
		Where("tenant_id=? AND opportunity_id=? AND deleted_at IS NULL AND status NOT IN ?", tenantID, opportunityID, []string{"COMPLETED", "REJECTED", "CANCELLED"}).
		Count(&activePresale).Error; err != nil {
		return nil, err
	}
	if activePresale > 0 {
		blockers = append(blockers, "ACTIVE_PRESALE_REQUEST")
	}
	var contractCount int64
	if err := db.Model(&Opportunity{}).
		Where("tenant_id=? AND id=? AND deleted_at IS NULL AND contract_ref IS NOT NULL AND contract_ref<>''", tenantID, opportunityID).
		Count(&contractCount).Error; err != nil {
		return nil, err
	}
	if contractCount > 0 {
		blockers = append(blockers, "LINKED_CONTRACT")
	}
	var activeExternal int64
	if err := db.Model(&ExternalLink{}).Where(
		"tenant_id=? AND opportunity_id=? AND status IN ? AND NOT EXISTS (SELECT 1 FROM crm_opportunity_external_links newer WHERE newer.tenant_id=crm_opportunity_external_links.tenant_id AND newer.opportunity_id=crm_opportunity_external_links.opportunity_id AND newer.source_id=crm_opportunity_external_links.source_id AND (newer.changed_at>crm_opportunity_external_links.changed_at OR (newer.changed_at=crm_opportunity_external_links.changed_at AND newer.id>crm_opportunity_external_links.id)))",
		tenantID, opportunityID, []string{"报价审批中", "报价已通过", "投标标书制作", "开标中", "投标开标中"},
	).Count(&activeExternal).Error; err != nil {
		return nil, err
	}
	if activeExternal > 0 {
		blockers = append(blockers, "ACTIVE_EXTERNAL_QB")
	}
	return blockers, nil
}

func scoped(db *gorm.DB, principal auth.Principal) *gorm.DB {
	db = db.Where("crm_opportunities.tenant_id=? AND crm_opportunities.deleted_at IS NULL", principal.TenantID)
	switch principal.ScopeMode {
	case auth.ScopeAll:
		return db
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return db.Where("1=0")
		}
		return db.Where("crm_opportunities.owner_org_id IN ?", principal.OrganizationIDs)
	default:
		return db.Where("crm_opportunities.owner_user_id=?", principal.UserID)
	}
}

func (r *GORMRepository) FindByID(ctx context.Context, principal auth.Principal, id uint64) (*Opportunity, error) {
	var model Opportunity
	if err := scoped(database.FromContext(ctx, r.db).Model(&Opportunity{}), principal).First(&model, "crm_opportunities.id=?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &model, nil
}

func (r *GORMRepository) FindByIDForUpdate(ctx context.Context, principal auth.Principal, id uint64) (*Opportunity, error) {
	var model Opportunity
	if err := scoped(database.FromContext(ctx, r.db).Model(&Opportunity{}), principal).
		Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "crm_opportunities.id=?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &model, nil
}

func (r *GORMRepository) List(ctx context.Context, principal auth.Principal, query ListQuery) (pagination.Page[Response], error) {
	query.Page, query.PageSize = pagination.Normalize(query.Page, query.PageSize)
	db := scoped(database.FromContext(ctx, r.db).Model(&Opportunity{}), principal)
	if query.Keyword != "" {
		db = db.Where("opportunity_no LIKE ? OR name LIKE ?", "%"+query.Keyword+"%", "%"+query.Keyword+"%")
	}
	if query.Stage != "" {
		db = db.Where("current_stage=?", query.Stage)
	}
	if query.Status != "" {
		db = db.Where("opp_status=?", query.Status)
	}
	if query.OwnerID != "" {
		db = db.Where("owner_user_id=?", query.OwnerID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[Response]{}, err
	}
	sortFields := map[string]string{"created_at": "created_at", "updated_at": "updated_at", "expected_amount": "expected_amount", "expected_sign_date": "expected_sign_date"}
	sortField := sortFields[query.SortBy]
	if sortField == "" {
		sortField = "updated_at"
	}
	direction := "DESC"
	if strings.EqualFold(query.SortOrder, "asc") {
		direction = "ASC"
	}
	var models []Opportunity
	if err := db.Order(sortField + " " + direction).Order("id DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&models).Error; err != nil {
		return pagination.Page[Response]{}, err
	}
	items := make([]Response, 0, len(models))
	for i := range models {
		items = append(items, toResponse(&models[i]))
	}
	return pagination.Page[Response]{Items: items, Page: query.Page, PageSize: query.PageSize, Total: total}, nil
}

func (r *GORMRepository) UpdateStage(ctx context.Context, model *Opportunity, expectedVersion uint64) error {
	updates := map[string]any{"current_stage": model.CurrentStage, "opp_status": model.Status, "contract_ref": model.ContractRef, "lost_reason": model.LostReason, "terminal_pending_type": model.TerminalPendingType, "stage_changed_at": model.StageChangedAt, "external_status_changed_at": model.ExternalStatusChangedAt, "updated_by": model.UpdatedBy, "version": gorm.Expr("version+1")}
	result := database.FromContext(ctx, r.db).Model(&Opportunity{}).Where("id=? AND tenant_id=? AND version=? AND opp_status<>? AND deleted_at IS NULL", model.ID, model.TenantID, expectedVersion, StatusVoid).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	model.Version = expectedVersion + 1
	return nil
}

func (r *GORMRepository) CreateStageLog(ctx context.Context, model *StageLog) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) CreateExternalLink(ctx context.Context, model *ExternalLink) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) FindExternalLink(ctx context.Context, tenantID string, opportunityID uint64, sourceID, status string) (*ExternalLink, error) {
	var model ExternalLink
	err := database.FromContext(ctx, r.db).Where("tenant_id=? AND opportunity_id=? AND source_id=? AND status=?", tenantID, opportunityID, sourceID, status).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

func (r *GORMRepository) LatestExternalLink(ctx context.Context, tenantID string, opportunityID uint64) (*ExternalLink, error) {
	var model ExternalLink
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND opportunity_id=?", tenantID, opportunityID).
		Order("changed_at DESC").Order("id DESC").Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

// LatestApprovedQuotation returns the newest quotation whose latest status for
// that source document is still approved. An older approval for a quotation
// that was subsequently invalidated must not be used for an amount warning.
func (r *GORMRepository) LatestApprovedQuotation(ctx context.Context, tenantID string, opportunityID uint64) (*ExternalLink, error) {
	var model ExternalLink
	err := latestApprovedQuotationQuery(database.FromContext(ctx, r.db), tenantID, opportunityID).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

func latestApprovedQuotationQuery(db *gorm.DB, tenantID string, opportunityID uint64) *gorm.DB {
	return db.Model(&ExternalLink{}).
		Where("crm_opportunity_external_links.tenant_id=? AND crm_opportunity_external_links.opportunity_id=? AND crm_opportunity_external_links.type=? AND crm_opportunity_external_links.status=?", tenantID, opportunityID, "报价", "报价已通过").
		Where(`NOT EXISTS (
			SELECT 1 FROM crm_opportunity_external_links AS newer
			WHERE newer.tenant_id=crm_opportunity_external_links.tenant_id
			  AND newer.opportunity_id=crm_opportunity_external_links.opportunity_id
			  AND newer.source_id=crm_opportunity_external_links.source_id
			  AND (newer.changed_at>crm_opportunity_external_links.changed_at
			       OR (newer.changed_at=crm_opportunity_external_links.changed_at AND newer.id>crm_opportunity_external_links.id))
		)`).
		Order("crm_opportunity_external_links.changed_at DESC").
		Order("crm_opportunity_external_links.id DESC")
}

func (r *GORMRepository) FindOutboxEvent(ctx context.Context, tenantID, eventID string) (*OutboxEvent, error) {
	var model OutboxEvent
	err := database.FromContext(ctx, r.db).Where("tenant_id=? AND event_id=?", tenantID, eventID).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}

func toResponse(model *Opportunity) Response {
	var endDate *string
	if model.EndDate != nil {
		value := model.EndDate.Format("2006-01-02")
		endDate = &value
	}
	return Response{ID: model.ID, OpportunityNo: model.OpportunityNo, Name: model.Name, CustomerID: model.CustomerID, Type: model.Type, Source: model.Source, ExpectedAmount: model.ExpectedAmount.StringFixed(2), ExpectedSignDate: model.ExpectedSignDate.Format("2006-01-02"), RequirementSummary: model.RequirementSummary, SystemCount: model.SystemCount, PainPoints: model.PainPoints, CompetitorInfo: model.CompetitorInfo, OwnerUserID: model.OwnerUserID, OwnerOrgID: model.OwnerOrgID, CurrentStage: model.CurrentStage, Status: model.Status, ContractRef: model.ContractRef, LostReason: model.LostReason, TerminalPendingType: model.TerminalPendingType, StageChangedAt: model.StageChangedAt, EndDate: endDate, StatusBeforeVoid: model.StatusBeforeVoid, Version: model.Version, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt}
}
