package customer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository interface {
	NextNumber(context.Context, string, string) (string, error)
	Create(context.Context, *Customer) error
	FindByID(context.Context, auth.Principal, uint64, bool) (*Customer, error)
	FindDuplicates(context.Context, string, string, string, uint64) ([]DuplicateCandidate, error)
	List(context.Context, auth.Principal, ListQuery) (pagination.Page[Response], error)
	Update(context.Context, *Customer, uint64) error
	ReplaceContacts(context.Context, *Customer) error
	UpdateStatus(context.Context, *Customer, uint64) error
	VoidBlockers(context.Context, string, uint64) ([]string, error)
	CreateChangeLog(context.Context, *ChangeLog) error
	CreateFollowup(context.Context, *Followup) error
	LockActiveForWrite(context.Context, auth.Principal, uint64) (*Customer, error)
	ListFollowups(context.Context, string, uint64, int, int) (pagination.Page[FollowupResponse], error)
	ListChangeLogs(context.Context, string, uint64, int, int) (pagination.Page[ChangeLogResponse], error)
	ListOpportunityHistory(context.Context, string, uint64, int, int) (pagination.Page[OpportunitySummary], error)
}

// CreateRepository 负责交互式创建所需的事务与持久化重放坐标。
// 将它与通用仓储分离，可避免批量导入的“逐行独立提交”语义被误改成整批事务。
type CreateRepository interface {
	WithCreateTransaction(context.Context, func(context.Context) error) error
	FindCreateIdempotency(context.Context, string, string, string) (*CreateIdempotency, error)
	FindCreatedCustomer(context.Context, string, string, uint64) (*Customer, error)
	CreateCreateIdempotency(context.Context, *CreateIdempotency) error
}

func (r *GORMRepository) LockActiveCustomerForProfile(ctx context.Context, principal auth.Principal, customerID uint64) (*Customer, error) {
	var model Customer
	err := scopedCustomer(database.FromContext(ctx, r.db).Model(&Customer{}), principal).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("crm_customers.id=? AND crm_customers.status=? AND crm_customers.merged_into_id IS NULL", customerID, StatusActive).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &model, err
}

func (r *GORMRepository) ListStakeholders(ctx context.Context, tenantID string, customerID uint64) ([]Stakeholder, error) {
	var models []Stakeholder
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, customerID).
		Order("sort_order ASC").Order("id ASC").Find(&models).Error
	return models, err
}

func (r *GORMRepository) ReplaceStakeholders(ctx context.Context, tenantID string, customerID uint64, actorID string, models []Stakeholder) error {
	db := database.FromContext(ctx, r.db)
	now := time.Now().UTC()
	if err := db.Model(&Stakeholder{}).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, customerID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now, "updated_by": actorID, "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	if len(models) == 0 {
		return nil
	}
	return db.Create(&models).Error
}

func (r *GORMRepository) ListInformationSystems(ctx context.Context, tenantID string, customerID uint64) ([]InformationSystem, error) {
	var models []InformationSystem
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, customerID).
		Order("sort_order ASC").Order("id ASC").Find(&models).Error
	return models, err
}

func (r *GORMRepository) ReplaceInformationSystems(ctx context.Context, tenantID string, customerID uint64, actorID string, models []InformationSystem) error {
	db := database.FromContext(ctx, r.db)
	now := time.Now().UTC()
	if err := db.Model(&InformationSystem{}).
		Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenantID, customerID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now, "updated_by": actorID, "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	if len(models) == 0 {
		return nil
	}
	return db.Create(&models).Error
}

func (r *GORMRepository) IncrementProfileVersion(ctx context.Context, tenantID string, customerID, expectedVersion uint64, actorID string) error {
	result := database.FromContext(ctx, r.db).Model(&Customer{}).
		Where("tenant_id=? AND id=? AND version=? AND status=? AND merged_into_id IS NULL AND deleted_at IS NULL", tenantID, customerID, expectedVersion, StatusActive).
		Updates(map[string]any{"updated_by": actorID, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (r *GORMRepository) CreateChangeLog(ctx context.Context, model *ChangeLog) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) CreateFollowup(ctx context.Context, model *Followup) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

// LockActiveForWrite 与客户合并共用同一行锁协议。跟进记录的插入和审计提交前一直持锁，
// 防止合并事务在并发窗口中迁移完关系后又出现写入原客户的新记录。
func (r *GORMRepository) LockActiveForWrite(ctx context.Context, principal auth.Principal, customerID uint64) (*Customer, error) {
	var model Customer
	err := scopedCustomer(database.FromContext(ctx, r.db).Model(&Customer{}), principal).
		Clauses(clause.Locking{Strength: "SHARE"}).
		Where("crm_customers.id=? AND crm_customers.status=? AND crm_customers.merged_into_id IS NULL", customerID, StatusActive).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &model, err
}

func (r *GORMRepository) ListFollowups(ctx context.Context, tenantID string, customerID uint64, page, pageSize int) (pagination.Page[FollowupResponse], error) {
	page, pageSize = pagination.Normalize(page, pageSize)
	db := database.FromContext(ctx, r.db).Model(&Followup{}).
		Where("tenant_id = ? AND customer_id = ? AND deleted_at IS NULL", tenantID, customerID)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	var models []Followup
	if err := db.Order("followed_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&models).Error; err != nil {
		return pagination.Page[FollowupResponse]{}, err
	}
	items := make([]FollowupResponse, 0, len(models))
	for i := range models {
		items = append(items, toFollowupResponse(&models[i]))
	}
	return pagination.Page[FollowupResponse]{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (r *GORMRepository) ListChangeLogs(ctx context.Context, tenantID string, customerID uint64, page, pageSize int) (pagination.Page[ChangeLogResponse], error) {
	page, pageSize = pagination.Normalize(page, pageSize)
	// 操作日志读取共享的追加式审计流，而不只读取字段差异表，因此创建、状态、跟进和合并
	// 都在同一个客户可见性边界下展示。
	db := database.FromContext(ctx, r.db).Table("crm_audit_events").
		Where("tenant_id = ? AND module = ? AND resource_type = ? AND resource_id = ?", tenantID, "customer", "customer", stringUint(customerID))
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[ChangeLogResponse]{}, err
	}
	type row struct {
		ID                    uint64
		Operation, Reason     string
		ActorID, Result       string
		RequestID             string
		BeforeJSON, AfterJSON []byte
		OccurredAt            time.Time
	}
	var models []row
	if err := db.Order("occurred_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&models).Error; err != nil {
		return pagination.Page[ChangeLogResponse]{}, err
	}
	items := make([]ChangeLogResponse, 0, len(models))
	for _, model := range models {
		items = append(items, ChangeLogResponse{ID: model.ID, Operation: model.Operation, BeforeJSON: model.BeforeJSON, AfterJSON: model.AfterJSON, Reason: model.Reason, ActorID: model.ActorID, Result: model.Result, RequestID: model.RequestID, OccurredAt: model.OccurredAt})
	}
	return pagination.Page[ChangeLogResponse]{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (r *GORMRepository) ListOpportunityHistory(ctx context.Context, tenantID string, customerID uint64, page, pageSize int) (pagination.Page[OpportunitySummary], error) {
	page, pageSize = pagination.Normalize(page, pageSize)
	db := database.FromContext(ctx, r.db).Table("crm_opportunities").
		Where("tenant_id = ? AND customer_id = ? AND deleted_at IS NULL", tenantID, customerID)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[OpportunitySummary]{}, err
	}
	type row struct {
		ID                                                     uint64
		OpportunityNo, Name, CurrentStage, Status, OwnerUserID string
		ExpectedAmount                                         decimal.Decimal
		CreatedAt, UpdatedAt                                   time.Time
	}
	var rows []row
	if err := db.Select("id, opportunity_no, name, expected_amount, current_stage, opp_status AS status, owner_user_id, created_at, updated_at").Order("created_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		return pagination.Page[OpportunitySummary]{}, err
	}
	items := make([]OpportunitySummary, 0, len(rows))
	for _, item := range rows {
		items = append(items, OpportunitySummary{ID: item.ID, OpportunityNo: item.OpportunityNo, Name: item.Name, ExpectedAmount: item.ExpectedAmount.StringFixed(2), CurrentStage: item.CurrentStage, Status: item.Status, OwnerUserID: item.OwnerUserID, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	return pagination.Page[OpportunitySummary]{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func toFollowupResponse(model *Followup) FollowupResponse {
	return FollowupResponse{ID: model.ID, Type: model.Type, Content: model.Content, FollowedAt: model.FollowedAt, FollowedBy: model.FollowedBy, NextFollowAt: model.NextFollowAt, CreatedAt: model.CreatedAt}
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }

func (r *GORMRepository) WithCreateTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
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

// FindCreatedCustomer 确认重放记录仍指向同租户、同操作者创建的资源。
// 这里不能套用当前负责人范围，因为 customer.create 本来就允许把新客户分配给其他负责人。
func (r *GORMRepository) FindCreatedCustomer(ctx context.Context, tenantID, actorID string, customerID uint64) (*Customer, error) {
	var model Customer
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND id=? AND created_by=? AND deleted_at IS NULL", tenantID, customerID, actorID).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &model, err
}

func (r *GORMRepository) CreateCreateIdempotency(ctx context.Context, model *CreateIdempotency) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

type sequence struct {
	TenantID     string `gorm:"primaryKey;size:64"`
	BusinessDate string `gorm:"primaryKey;size:8"`
	BusinessType string `gorm:"primaryKey;size:32"`
	CurrentValue uint64
}

func (sequence) TableName() string { return "crm_biz_sequences" }

func (r *GORMRepository) NextNumber(ctx context.Context, tenantID, date string) (string, error) {
	db := database.FromContext(ctx, r.db)
	row := sequence{TenantID: tenantID, BusinessDate: date, BusinessType: "CUSTOMER", CurrentValue: 1}
	// LAST_INSERT_ID(expr) 绑定当前数据库连接返回本次原子增量值，避免先读后写在并发创建时发出重复编号。
	result := db.Exec(`INSERT INTO crm_biz_sequences (tenant_id,business_date,business_type,current_value)
		VALUES (?,?,?,LAST_INSERT_ID(1)) ON DUPLICATE KEY UPDATE current_value=LAST_INSERT_ID(current_value+1)`, row.TenantID, row.BusinessDate, row.BusinessType)
	if result.Error != nil {
		return "", result.Error
	}
	var current uint64
	if err := db.Raw("SELECT LAST_INSERT_ID()").Scan(&current).Error; err != nil {
		return "", err
	}
	suffix := leftPad4(current)
	if suffix == "" {
		return "", errors.New("daily customer sequence exhausted")
	}
	return "KH" + date + suffix, nil
}

func leftPad4(value uint64) string {
	if value > 9999 {
		return ""
	}
	digits := "0000" + strings.TrimSpace(stringUint(value))
	if len(digits) > 4 {
		return digits[len(digits)-4:]
	}
	return digits
}

func stringUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 20)
	for value > 0 {
		buffer = append(buffer, byte('0'+value%10))
		value /= 10
	}
	for left, right := 0, len(buffer)-1; left < right; left, right = left+1, right-1 {
		buffer[left], buffer[right] = buffer[right], buffer[left]
	}
	return string(buffer)
}

func (r *GORMRepository) Create(ctx context.Context, customer *Customer) error {
	return database.FromContext(ctx, r.db).Create(customer).Error
}

func scopedCustomer(db *gorm.DB, principal auth.Principal) *gorm.DB {
	// 租户条件始终存在；组织范围必须使用令牌中经平台确认的组织集合，空集合按无权限处理，
	// 不能退化为租户全量或信任请求参数中的组织标识。
	db = db.Where("crm_customers.tenant_id = ? AND crm_customers.deleted_at IS NULL", principal.TenantID)
	switch principal.ScopeMode {
	case auth.ScopeAll:
		return db
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return db.Where("1 = 0")
		}
		return db.Where("crm_customers.owner_org_id IN ?", principal.OrganizationIDs)
	default:
		return db.Where("crm_customers.owner_user_id = ?", principal.UserID)
	}
}

func (r *GORMRepository) FindByID(ctx context.Context, principal auth.Principal, id uint64, preload bool) (*Customer, error) {
	db := scopedCustomer(database.FromContext(ctx, r.db).Model(&Customer{}), principal)
	if preload {
		db = db.Preload("Contacts", "deleted_at IS NULL")
	}
	var model Customer
	if err := db.First(&model, "crm_customers.id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &model, nil
}

func (r *GORMRepository) FindDuplicates(ctx context.Context, tenantID, normalizedName, creditHMAC string, excludeID uint64) ([]DuplicateCandidate, error) {
	type row struct {
		ID                                              uint64
		CustomerNo, Name, Status, UnifiedCreditCodeHMAC string
	}
	query := database.FromContext(ctx, r.db).Table("crm_customers").Select("id,customer_no,name,status,unified_credit_code_hmac").Where("tenant_id = ? AND deleted_at IS NULL", tenantID)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	if creditHMAC != "" {
		query = query.Where("unified_credit_code_hmac = ? OR normalized_name LIKE ?", creditHMAC, "%"+normalizedName+"%")
	} else {
		query = query.Where("normalized_name LIKE ?", "%"+normalizedName+"%")
	}
	var rows []row
	if err := query.Limit(20).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]DuplicateCandidate, 0, len(rows))
	for _, item := range rows {
		result = append(result, DuplicateCandidate{ID: item.ID, CustomerNo: item.CustomerNo, Name: item.Name, Status: item.Status, ExactCode: creditHMAC != "" && item.UnifiedCreditCodeHMAC == creditHMAC})
	}
	return result, nil
}

func (r *GORMRepository) Update(ctx context.Context, model *Customer, expectedVersion uint64) error {
	updates := map[string]any{"name": model.Name, "normalized_name": model.NormalizedName, "unified_credit_code_cipher": model.UnifiedCreditCodeCipher, "unified_credit_code_hmac": model.UnifiedCreditCodeHMAC, "customer_type": model.CustomerType, "industry": model.Industry, "region": model.Region, "owner_user_id": model.OwnerUserID, "owner_org_id": model.OwnerOrgID, "updated_by": model.UpdatedBy, "version": gorm.Expr("version + 1")}
	result := database.FromContext(ctx, r.db).Model(&Customer{}).Where("id = ? AND tenant_id = ? AND version = ? AND status = ? AND deleted_at IS NULL", model.ID, model.TenantID, expectedVersion, StatusActive).Updates(updates)
	if result.Error != nil {
		return mapWriteError(result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	model.Version = expectedVersion + 1
	return nil
}

func (r *GORMRepository) ReplaceContacts(ctx context.Context, model *Customer) error {
	db := database.FromContext(ctx, r.db)
	if err := db.Where("tenant_id = ? AND customer_id = ? AND deleted_at IS NULL", model.TenantID, model.ID).Delete(&Contact{}).Error; err != nil {
		return err
	}
	for i := range model.Contacts {
		model.Contacts[i].ID = 0
		model.Contacts[i].CustomerID = model.ID
	}
	if len(model.Contacts) == 0 {
		return nil
	}
	return db.Create(&model.Contacts).Error
}

func (r *GORMRepository) UpdateStatus(ctx context.Context, model *Customer, expectedVersion uint64) error {
	expectedStatus := StatusActive
	if model.Status == StatusActive {
		expectedStatus = StatusVoid
	}
	result := database.FromContext(ctx, r.db).Model(&Customer{}).Where("id = ? AND tenant_id = ? AND version = ? AND status = ? AND deleted_at IS NULL", model.ID, model.TenantID, expectedVersion, expectedStatus).Updates(map[string]any{"status": model.Status, "end_date": model.EndDate, "updated_by": model.UpdatedBy, "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	model.Version = expectedVersion + 1
	return nil
}

func (r *GORMRepository) VoidBlockers(ctx context.Context, tenantID string, customerID uint64) ([]string, error) {
	db := database.FromContext(ctx, r.db)
	checks := []struct{ table, condition, code string }{
		{"crm_opportunities", "opp_status = 'FOLLOWING'", "ACTIVE_OPPORTUNITY"},
		{"crm_portal_invites", "status = 'PENDING'", "ACTIVE_PORTAL_INVITE"},
	}
	blockers := make([]string, 0, len(checks))
	for _, check := range checks {
		var count int64
		err := db.Table(check.table).Where("tenant_id = ? AND customer_id = ? AND deleted_at IS NULL AND "+check.condition, tenantID, customerID).Count(&count).Error
		if err != nil {
			return nil, err
		}
		if count > 0 {
			blockers = append(blockers, check.code)
		}
	}
	// 售前记录关联的是 opportunity_id 而不是 customer_id，阻断检查必须沿真实关系连接，
	// 不能假设售前表存在客户字段而漏掉依赖。
	var activePresale int64
	err := db.Table("crm_presale_requests AS pr").Joins("JOIN crm_opportunities AS o ON o.id = pr.opportunity_id AND o.tenant_id = pr.tenant_id AND o.deleted_at IS NULL").Where("pr.tenant_id = ? AND o.customer_id = ? AND pr.deleted_at IS NULL AND pr.status NOT IN ('COMPLETED','CANCELLED','REJECTED')", tenantID, customerID).Count(&activePresale).Error
	if err != nil {
		return nil, err
	}
	if activePresale > 0 {
		blockers = append(blockers, "ACTIVE_PRESALE_REQUEST")
	}
	return blockers, nil
}

func (r *GORMRepository) List(ctx context.Context, principal auth.Principal, query ListQuery) (pagination.Page[Response], error) {
	query.Page, query.PageSize = pagination.Normalize(query.Page, query.PageSize)
	db := buildCustomerListQuery(database.FromContext(ctx, r.db), principal, query)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return pagination.Page[Response]{}, err
	}
	sortFields := map[string]string{"created_at": "crm_customers.created_at", "updated_at": "crm_customers.updated_at", "name": "crm_customers.name", "last_followup_at": "customer_followup_summary.last_followup_at", "opportunity_amount_sum": "customer_opportunity_summary.opportunity_amount_sum"}
	sortField := sortFields[query.SortBy]
	if sortField == "" {
		sortField = "crm_customers.updated_at"
	}
	order := "DESC"
	if strings.EqualFold(query.SortOrder, "asc") {
		order = "ASC"
	}
	type listRow struct {
		Customer
		LastFollowupAt       *time.Time      `gorm:"column:last_followup_at"`
		OpportunityAmountSum decimal.Decimal `gorm:"column:opportunity_amount_sum"`
	}
	var models []listRow
	if err := db.Select("crm_customers.*, customer_followup_summary.last_followup_at, COALESCE(customer_opportunity_summary.opportunity_amount_sum, 0) AS opportunity_amount_sum").Order(sortField + " " + order).Order("crm_customers.id " + order).Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Scan(&models).Error; err != nil {
		return pagination.Page[Response]{}, err
	}
	items := make([]Response, 0, len(models))
	for i := range models {
		item := toResponse(&models[i].Customer)
		item.LastFollowupAt = models[i].LastFollowupAt
		item.OpportunityAmountSum = models[i].OpportunityAmountSum.StringFixed(2)
		items = append(items, item)
	}
	return pagination.Page[Response]{Items: items, Page: query.Page, PageSize: query.PageSize, Total: total}, nil
}

func buildCustomerListQuery(db *gorm.DB, principal auth.Principal, query ListQuery) *gorm.DB {
	db = scopedCustomer(db.Model(&Customer{}), principal).
		Joins(`LEFT JOIN (
			SELECT tenant_id, customer_id, MAX(followed_at) AS last_followup_at
			FROM crm_customer_followups WHERE tenant_id = ? AND deleted_at IS NULL GROUP BY tenant_id, customer_id
		) AS customer_followup_summary ON customer_followup_summary.tenant_id = crm_customers.tenant_id AND customer_followup_summary.customer_id = crm_customers.id`, principal.TenantID).
		Joins(`LEFT JOIN (
			SELECT tenant_id, customer_id, SUM(expected_amount) AS opportunity_amount_sum
			FROM crm_opportunities WHERE tenant_id = ? AND deleted_at IS NULL AND opp_status <> 'VOID' GROUP BY tenant_id, customer_id
		) AS customer_opportunity_summary ON customer_opportunity_summary.tenant_id = crm_customers.tenant_id AND customer_opportunity_summary.customer_id = crm_customers.id`, principal.TenantID)
	if query.Keyword != "" {
		normalized := normalizeName(query.Keyword)
		db = db.Where("customer_no LIKE ? OR normalized_name LIKE ?", "%"+query.Keyword+"%", "%"+normalized+"%")
	}
	if query.CustomerType != "" {
		db = db.Where("customer_type = ?", query.CustomerType)
	}
	if query.Industry != "" {
		db = db.Where("industry = ?", query.Industry)
	}
	if query.Region != "" {
		db = db.Where("region = ?", query.Region)
	}
	if query.Status != "" {
		db = db.Where("status = ?", query.Status)
	}
	if query.OwnerID != "" {
		db = db.Where("owner_user_id = ?", query.OwnerID)
	}
	if query.CreatedFrom != nil {
		db = db.Where("crm_customers.created_at >= ?", *query.CreatedFrom)
	}
	if query.CreatedTo != nil {
		db = db.Where("crm_customers.created_at < ?", *query.CreatedTo)
	}
	if query.LastFollowupFrom != nil {
		db = db.Where("customer_followup_summary.last_followup_at >= ?", *query.LastFollowupFrom)
	}
	if query.LastFollowupTo != nil {
		db = db.Where("customer_followup_summary.last_followup_at < ?", *query.LastFollowupTo)
	}
	switch query.QuickFilter {
	case QuickFilterNew:
		db = db.Where("crm_customers.created_at >= ?", query.Now.AddDate(0, 0, -30))
	case QuickFilterWon:
		db = db.Where(`EXISTS (
			SELECT 1 FROM crm_opportunities won
			WHERE won.tenant_id = crm_customers.tenant_id AND won.customer_id = crm_customers.id
			AND won.deleted_at IS NULL AND won.opp_status = 'CLOSED' AND won.current_stage = '已签约'
		)`)
	case QuickFilterFollowupDue:
		// 仅检查每个客户时间线上最新一条跟进；历史记录中的 next_follow_at 到期，
		// 若已有更新跟进覆盖，不能继续把客户标成待跟进。
		db = db.Where(`EXISTS (
			SELECT 1 FROM crm_customer_followups due
			WHERE due.tenant_id = crm_customers.tenant_id AND due.customer_id = crm_customers.id
			AND due.deleted_at IS NULL AND due.next_follow_at IS NOT NULL AND due.next_follow_at <= ?
			AND NOT EXISTS (
				SELECT 1 FROM crm_customer_followups newer
				WHERE newer.tenant_id = due.tenant_id AND newer.customer_id = due.customer_id AND newer.deleted_at IS NULL
				AND (newer.followed_at > due.followed_at OR (newer.followed_at = due.followed_at AND newer.id > due.id))
			)
		)`, query.Now)
	}
	return db
}

func normalizeName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), ""))
}

func toResponse(model *Customer) Response {
	contacts := make([]ContactResponse, 0, len(model.Contacts))
	for _, contact := range model.Contacts {
		contacts = append(contacts, ContactResponse{ID: contact.ID, Name: contact.Name, PhoneMasked: contact.PhoneMasked, EmailMasked: contact.EmailMasked, IsRegistration: contact.IsRegistration})
	}
	var endDate *string
	if model.EndDate != nil {
		value := model.EndDate.UTC().Format("2006-01-02")
		endDate = &value
	}
	return Response{ID: model.ID, CustomerNo: model.CustomerNo, Name: model.Name, CustomerType: model.CustomerType, Industry: model.Industry, Region: model.Region, OwnerUserID: model.OwnerUserID, OwnerOrgID: model.OwnerOrgID, Status: model.Status, EndDate: endDate, MergedIntoID: model.MergedIntoID, Contacts: contacts, Version: model.Version, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt, OpportunityAmountSum: "0.00"}
}
