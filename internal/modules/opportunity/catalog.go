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
	"unicode/utf8"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CatalogKind string

const (
	CatalogKindType         CatalogKind = "TYPE"
	CatalogKindSource       CatalogKind = "SOURCE"
	catalogManagePermission             = "opportunity.catalog.manage"
	maxCatalogSelections                = 50
)

var (
	ErrCatalogInvalid     = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_CATALOG_INVALID", "opportunity catalog item is invalid")
	ErrCatalogNotFound    = apperror.New(http.StatusNotFound, "CRM_OPPORTUNITY_CATALOG_NOT_FOUND", "opportunity catalog item not found")
	ErrCatalogInUse       = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_CATALOG_IN_USE", "opportunity catalog item is referenced and cannot be deleted")
	ErrCatalogDuplicate   = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_CATALOG_DUPLICATE", "opportunity catalog name already exists")
	ErrCatalogUnavailable = apperror.New(http.StatusServiceUnavailable, "CRM_OPPORTUNITY_CATALOG_UNAVAILABLE", "opportunity catalog is unavailable")
)

type CatalogItem struct {
	ID             uint64      `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID       string      `gorm:"size:64;not null" json:"-"`
	Kind           CatalogKind `gorm:"size:16;not null" json:"kind"`
	Code           string      `gorm:"size:32;not null" json:"code"`
	Name           string      `gorm:"size:64;not null" json:"name"`
	NormalizedName string      `gorm:"size:64;not null" json:"-"`
	SortOrder      uint32      `gorm:"not null" json:"sort_order"`
	Enabled        bool        `gorm:"not null" json:"enabled"`
	Version        uint64      `gorm:"not null" json:"version"`
	CreatedBy      string      `gorm:"size:64;not null" json:"created_by"`
	UpdatedBy      string      `gorm:"size:64;not null" json:"updated_by"`
	CreatedAt      time.Time   `gorm:"precision:3;not null" json:"created_at"`
	UpdatedAt      time.Time   `gorm:"precision:3;not null" json:"updated_at"`
	ReferenceCount int64       `gorm:"-" json:"reference_count"`
}

func (CatalogItem) TableName() string { return "crm_opportunity_catalog_items" }

type CatalogLink struct {
	TenantID      string      `gorm:"size:64;primaryKey"`
	OpportunityID uint64      `gorm:"primaryKey"`
	CatalogItemID uint64      `gorm:"primaryKey"`
	Kind          CatalogKind `gorm:"size:16;primaryKey"`
	SortOrder     uint32      `gorm:"not null"`
	CreatedAt     time.Time   `gorm:"precision:3;not null"`
}

func (CatalogLink) TableName() string { return "crm_opportunity_catalog_links" }

type catalogInitialization struct {
	TenantID      string    `gorm:"size:64;primaryKey"`
	InitializedAt time.Time `gorm:"precision:3;not null"`
}

func (catalogInitialization) TableName() string {
	return "crm_opportunity_catalog_initializations"
}

type catalogIdempotency struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null"`
	ActorID        string    `gorm:"size:64;not null"`
	Operation      string    `gorm:"size:16;not null"`
	IdempotencyKey string    `gorm:"size:128;not null"`
	RequestHash    string    `gorm:"size:64;not null"`
	ResponseJSON   []byte    `gorm:"type:json;not null"`
	CreatedAt      time.Time `gorm:"precision:3;not null"`
}

func (catalogIdempotency) TableName() string { return "crm_opportunity_catalog_idempotency" }

type CatalogCreateRequest struct {
	Kind           CatalogKind `json:"kind"`
	Name           string      `json:"name"`
	SortOrder      uint32      `json:"sort_order"`
	IdempotencyKey string      `json:"-"`
}

type CatalogUpdateRequest struct {
	Name      string `json:"name"`
	SortOrder uint32 `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
	Version   uint64 `json:"version"`
	Reason    string `json:"reason"`
}

type CatalogDeleteRequest struct {
	Version        uint64 `json:"version"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"-"`
}

type CatalogDeleteResponse struct {
	ID      uint64 `json:"id"`
	Deleted bool   `json:"deleted"`
}

type OpportunityCatalogValue struct {
	ID      uint64 `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type CatalogService struct {
	db    *gorm.DB
	audit audit.Writer
	now   func() time.Time
}

func NewCatalogService(db *gorm.DB, writer audit.Writer) *CatalogService {
	return &CatalogService{db: db, audit: writer, now: func() time.Time { return time.Now().UTC() }}
}

var defaultCatalogItems = map[CatalogKind][]string{
	CatalogKindType: {
		"等保审查", "密码应用安全性评估", "软件测试", "源代码审计", "渗透测试", "漏洞扫描",
		"APP安全完整性", "在线测试", "安全系数", "网络安全风险评估", "缺口分析", "机房检测",
		"网络安全检测服务", "安全培训", "安全性测试", "应急响应服务", "网络安全攻防演内容", "安全运维",
	},
	CatalogKindSource: {
		"客户主动咨询", "老客户复购/续约", "老客户转介绍", "公开招标", "销售开拓", "合作伙伴推荐",
		"展会/活动", "政府/主管单位指派", "线上渠道", "内部转介",
	},
}

func validCatalogKind(kind CatalogKind) bool {
	return kind == CatalogKindType || kind == CatalogKindSource
}

func normalizeCatalogName(value string) (string, string, error) {
	name := strings.TrimSpace(value)
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.Contains(name, "、") || strings.ContainsAny(name, "\r\n\t") {
		return "", "", ErrCatalogInvalid
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(name), ""))
	if normalized == "" {
		return "", "", ErrCatalogInvalid
	}
	return name, normalized, nil
}

func catalogCode(tenant string, kind CatalogKind, name string) string {
	digest := sha256.Sum256([]byte(tenant + ":" + string(kind) + ":" + name + ":" + request.NewID()))
	prefix := "OT-"
	if kind == CatalogKindSource {
		prefix = "OS-"
	}
	return prefix + hex.EncodeToString(digest[:12])
}

func (s *CatalogService) ensureDefaults(ctx context.Context, tenantID string) error {
	db := database.FromContext(ctx, s.db)
	return db.Transaction(func(tx *gorm.DB) error {
		now := s.now()
		marker := catalogInitialization{TenantID: tenantID, InitializedAt: now}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&marker)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			return nil
		}
		rows := make([]CatalogItem, 0, 28)
		for _, kind := range []CatalogKind{CatalogKindType, CatalogKindSource} {
			for index, name := range defaultCatalogItems[kind] {
				_, normalized, _ := normalizeCatalogName(name)
				digest := sha256.Sum256([]byte(tenantID + ":" + string(kind) + ":" + name))
				prefix := "OT-"
				if kind == CatalogKindSource {
					prefix = "OS-"
				}
				rows = append(rows, CatalogItem{TenantID: tenantID, Kind: kind, Code: prefix + hex.EncodeToString(digest[:12]), Name: name, NormalizedName: normalized, SortOrder: uint32((index + 1) * 10), Enabled: true, Version: 1, CreatedBy: "system:tenant-bootstrap", UpdatedBy: "system:tenant-bootstrap", CreatedAt: now, UpdatedAt: now})
			}
		}
		return tx.Create(&rows).Error
	})
}

func (s *CatalogService) List(ctx context.Context, kind CatalogKind, includeDisabled bool) ([]CatalogItem, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !validCatalogKind(kind) {
		return nil, ErrCatalogInvalid
	}
	if includeDisabled && !principal.HasPermission(catalogManagePermission) {
		return nil, apperror.ErrForbidden
	}
	if err = s.ensureDefaults(ctx, principal.TenantID); err != nil {
		return nil, err
	}
	query := database.FromContext(ctx, s.db).Table("crm_opportunity_catalog_items AS item").
		Select("item.*, COUNT(link.catalog_item_id) AS reference_count").
		Joins("LEFT JOIN crm_opportunity_catalog_links AS link ON link.tenant_id=item.tenant_id AND link.catalog_item_id=item.id").
		Where("item.tenant_id=? AND item.kind=?", principal.TenantID, kind)
	if !includeDisabled {
		query = query.Where("item.enabled=TRUE")
	}
	var rows []CatalogItem
	if err = query.Group("item.id").Order("item.sort_order ASC,item.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func catalogRequestHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateCatalogIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrIdempotencyRequired
	}
	if len(value) > 128 {
		return "", ErrIdempotencyKeyTooLong
	}
	return value, nil
}

func (s *CatalogService) Create(ctx context.Context, input CatalogCreateRequest) (*CatalogItem, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.HasPermission(catalogManagePermission) {
		return nil, apperror.ErrForbidden
	}
	if !validCatalogKind(input.Kind) {
		return nil, ErrCatalogInvalid
	}
	name, normalized, err := normalizeCatalogName(input.Name)
	if err != nil {
		return nil, err
	}
	key, err := validateCatalogIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := catalogRequestHash(struct {
		Kind      CatalogKind
		Name      string
		SortOrder uint32
	}{input.Kind, name, input.SortOrder})
	if err != nil {
		return nil, err
	}
	var result CatalogItem
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if defaultErr := s.ensureDefaults(txCtx, principal.TenantID); defaultErr != nil {
			return defaultErr
		}
		prior, replayErr := s.findIdempotency(txCtx, principal, "CREATE", key)
		if replayErr != nil {
			return replayErr
		}
		if prior != nil {
			if prior.RequestHash != requestHash || json.Unmarshal(prior.ResponseJSON, &result) != nil {
				return ErrIdempotencyConflict
			}
			return nil
		}
		now := s.now()
		result = CatalogItem{TenantID: principal.TenantID, Kind: input.Kind, Code: catalogCode(principal.TenantID, input.Kind, normalized), Name: name, NormalizedName: normalized, SortOrder: input.SortOrder, Enabled: true, Version: 1, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, CreatedAt: now, UpdatedAt: now}
		if createErr := database.FromContext(txCtx, s.db).Create(&result).Error; createErr != nil {
			if isMySQLDuplicate(createErr) {
				return ErrCatalogDuplicate
			}
			return createErr
		}
		if writeErr := s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity_catalog", Operation: "CREATE", ResourceType: "opportunity_catalog_item", ResourceID: uintString(result.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(result), Result: "SUCCESS"}); writeErr != nil {
			return writeErr
		}
		return s.saveIdempotency(txCtx, principal, "CREATE", key, requestHash, result)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *CatalogService) Update(ctx context.Context, id uint64, input CatalogUpdateRequest) (*CatalogItem, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.HasPermission(catalogManagePermission) {
		return nil, apperror.ErrForbidden
	}
	name, normalized, err := normalizeCatalogName(input.Name)
	if err != nil || input.Version == 0 || strings.TrimSpace(input.Reason) == "" || len(strings.TrimSpace(input.Reason)) > 500 {
		return nil, ErrCatalogInvalid
	}
	var result CatalogItem
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		db := database.FromContext(txCtx, s.db)
		var current CatalogItem
		if takeErr := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", principal.TenantID, id).Take(&current).Error; takeErr != nil {
			if errors.Is(takeErr, gorm.ErrRecordNotFound) {
				return ErrCatalogNotFound
			}
			return takeErr
		}
		if current.Version != input.Version {
			return ErrVersionConflict
		}
		result = current
		result.Name, result.NormalizedName, result.SortOrder, result.Enabled = name, normalized, input.SortOrder, input.Enabled
		result.UpdatedBy, result.UpdatedAt, result.Version = principal.UserID, s.now(), current.Version+1
		update := db.Model(&CatalogItem{}).Where("tenant_id=? AND id=? AND version=?", principal.TenantID, id, input.Version).Updates(map[string]any{"name": name, "normalized_name": normalized, "sort_order": input.SortOrder, "enabled": input.Enabled, "updated_by": principal.UserID, "updated_at": result.UpdatedAt, "version": gorm.Expr("version+1")})
		if update.Error != nil {
			if isMySQLDuplicate(update.Error) {
				return ErrCatalogDuplicate
			}
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrVersionConflict
		}
		if current.Name != name {
			if syncErr := syncOpportunityCatalogProjection(db, principal.TenantID, id, current.Kind); syncErr != nil {
				return syncErr
			}
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity_catalog", Operation: "UPDATE", ResourceType: "opportunity_catalog_item", ResourceID: uintString(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(current), AfterJSON: audit.JSON(result), Reason: strings.TrimSpace(input.Reason), Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *CatalogService) Delete(ctx context.Context, id uint64, input CatalogDeleteRequest) (*CatalogDeleteResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.HasPermission(catalogManagePermission) {
		return nil, apperror.ErrForbidden
	}
	key, err := validateCatalogIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if input.Version == 0 || strings.TrimSpace(input.Reason) == "" || len(strings.TrimSpace(input.Reason)) > 500 {
		return nil, ErrCatalogInvalid
	}
	requestHash, err := catalogRequestHash(struct {
		ID, Version uint64
		Reason      string
	}{id, input.Version, strings.TrimSpace(input.Reason)})
	if err != nil {
		return nil, err
	}
	result := CatalogDeleteResponse{ID: id, Deleted: true}
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		prior, replayErr := s.findIdempotency(txCtx, principal, "DELETE", key)
		if replayErr != nil {
			return replayErr
		}
		if prior != nil {
			if prior.RequestHash != requestHash || json.Unmarshal(prior.ResponseJSON, &result) != nil {
				return ErrIdempotencyConflict
			}
			return nil
		}
		db := database.FromContext(txCtx, s.db)
		var current CatalogItem
		if takeErr := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", principal.TenantID, id).Take(&current).Error; takeErr != nil {
			if errors.Is(takeErr, gorm.ErrRecordNotFound) {
				return ErrCatalogNotFound
			}
			return takeErr
		}
		if current.Version != input.Version {
			return ErrVersionConflict
		}
		var reference struct {
			Count int64 `gorm:"column:reference_count"`
		}
		// 锁定当前关联集合并使用当前读，避免事务早期的幂等查询在 REPEATABLE READ 下
		// 固化旧快照，进而漏掉等待目录项锁期间刚提交的商机关联。
		if countErr := db.Raw("SELECT COUNT(*) AS reference_count FROM crm_opportunity_catalog_links WHERE tenant_id=? AND catalog_item_id=? FOR UPDATE", principal.TenantID, id).Scan(&reference).Error; countErr != nil {
			return countErr
		}
		if reference.Count > 0 {
			return apperror.WithDetails(ErrCatalogInUse, map[string]any{"reference_count": reference.Count})
		}
		if auditErr := s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "opportunity_catalog", Operation: "DELETE", ResourceType: "opportunity_catalog_item", ResourceID: uintString(id), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(current), Reason: strings.TrimSpace(input.Reason), Result: "SUCCESS"}); auditErr != nil {
			return auditErr
		}
		deleted := db.Where("tenant_id=? AND id=? AND version=?", principal.TenantID, id, input.Version).Delete(&CatalogItem{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return ErrVersionConflict
		}
		return s.saveIdempotency(txCtx, principal, "DELETE", key, requestHash, result)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *CatalogService) findIdempotency(ctx context.Context, principal auth.Principal, operation, key string) (*catalogIdempotency, error) {
	var row catalogIdempotency
	err := database.FromContext(ctx, s.db).Where("tenant_id=? AND actor_id=? AND operation=? AND idempotency_key=?", principal.TenantID, principal.UserID, operation, key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

func (s *CatalogService) saveIdempotency(ctx context.Context, principal auth.Principal, operation, key, hash string, response any) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return database.FromContext(ctx, s.db).Create(&catalogIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Operation: operation, IdempotencyKey: key, RequestHash: hash, ResponseJSON: encoded, CreatedAt: s.now()}).Error
}

func isMySQLDuplicate(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func syncOpportunityCatalogProjection(db *gorm.DB, tenantID string, itemID uint64, kind CatalogKind) error {
	column := "type"
	if kind == CatalogKindSource {
		column = "source"
	}
	var opportunityIDs []uint64
	if err := db.Model(&CatalogLink{}).
		Where("tenant_id=? AND catalog_item_id=? AND kind=?", tenantID, itemID, kind).
		Pluck("opportunity_id", &opportunityIDs).Error; err != nil {
		return err
	}
	for _, opportunityID := range opportunityIDs {
		var names []string
		if err := db.Table("crm_opportunity_catalog_links AS link").
			Select("item.name").
			Joins("JOIN crm_opportunity_catalog_items AS item ON item.tenant_id=link.tenant_id AND item.id=link.catalog_item_id").
			Where("link.tenant_id=? AND link.opportunity_id=? AND link.kind=?", tenantID, opportunityID, kind).
			Order("link.sort_order ASC,item.id ASC").
			Pluck("item.name", &names).Error; err != nil {
			return err
		}
		if err := db.Model(&Opportunity{}).
			Where("tenant_id=? AND id=?", tenantID, opportunityID).
			UpdateColumn(column, strings.Join(names, "、")).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *CatalogService) ResolveSelections(ctx context.Context, tenantID string, kind CatalogKind, ids []uint64, legacy string, opportunityID uint64) ([]CatalogItem, string, error) {
	if err := s.ensureDefaults(ctx, tenantID); err != nil {
		return nil, "", err
	}
	ids = uniqueCatalogIDs(ids)
	if len(ids) == 0 {
		parts := splitCatalogText(legacy)
		if len(parts) == 0 {
			return nil, "", ErrCatalogInvalid
		}
		var rows []CatalogItem
		if err := database.FromContext(ctx, s.db).Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND kind=? AND enabled=TRUE AND normalized_name IN ?", tenantID, kind, normalizedCatalogNames(parts)).Find(&rows).Error; err != nil {
			return nil, "", err
		}
		byName := make(map[string]CatalogItem, len(rows))
		for _, row := range rows {
			byName[row.NormalizedName] = row
		}
		ordered := make([]CatalogItem, 0, len(parts))
		for _, part := range parts {
			_, normalized, err := normalizeCatalogName(part)
			if err != nil {
				return nil, "", err
			}
			row, ok := byName[normalized]
			if !ok {
				return nil, "", ErrCatalogInvalid
			}
			ordered = append(ordered, row)
		}
		return ordered, joinCatalogNames(ordered), nil
	}
	if len(ids) > maxCatalogSelections {
		return nil, "", ErrCatalogInvalid
	}
	var rows []CatalogItem
	if err := database.FromContext(ctx, s.db).Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND kind=? AND id IN ?", tenantID, kind, ids).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	if len(rows) != len(ids) {
		return nil, "", ErrCatalogInvalid
	}
	byID := make(map[uint64]CatalogItem, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	existing := map[uint64]bool{}
	if opportunityID != 0 {
		var existingIDs []uint64
		if err := database.FromContext(ctx, s.db).Model(&CatalogLink{}).Where("tenant_id=? AND opportunity_id=? AND kind=?", tenantID, opportunityID, kind).Pluck("catalog_item_id", &existingIDs).Error; err != nil {
			return nil, "", err
		}
		for _, id := range existingIDs {
			existing[id] = true
		}
	}
	ordered := make([]CatalogItem, 0, len(ids))
	for _, id := range ids {
		row := byID[id]
		if !row.Enabled && !existing[id] {
			return nil, "", ErrCatalogInvalid
		}
		ordered = append(ordered, row)
	}
	return ordered, joinCatalogNames(ordered), nil
}

func (s *CatalogService) ReplaceLinks(ctx context.Context, tenantID string, opportunityID uint64, kind CatalogKind, rows []CatalogItem) error {
	db := database.FromContext(ctx, s.db)
	if err := db.Where("tenant_id=? AND opportunity_id=? AND kind=?", tenantID, opportunityID, kind).Delete(&CatalogLink{}).Error; err != nil {
		return err
	}
	links := make([]CatalogLink, 0, len(rows))
	now := s.now()
	for index, row := range rows {
		links = append(links, CatalogLink{TenantID: tenantID, OpportunityID: opportunityID, CatalogItemID: row.ID, Kind: kind, SortOrder: uint32(index + 1), CreatedAt: now})
	}
	if len(links) == 0 {
		return ErrCatalogInvalid
	}
	return db.Create(&links).Error
}

func (s *CatalogService) EnrichResponses(ctx context.Context, tenantID string, values []Response) error {
	if len(values) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(values))
	positions := make(map[uint64]int, len(values))
	for index := range values {
		ids = append(ids, values[index].ID)
		positions[values[index].ID] = index
		values[index].Types = []OpportunityCatalogValue{}
		values[index].Sources = []OpportunityCatalogValue{}
	}
	type row struct {
		OpportunityID uint64
		CatalogItemID uint64
		Kind          CatalogKind
		Code          string
		Name          string
		Enabled       bool
		SortOrder     uint32
	}
	var rows []row
	err := database.FromContext(ctx, s.db).Table("crm_opportunity_catalog_links AS link").Select("link.opportunity_id,link.catalog_item_id,link.kind,item.code,item.name,item.enabled,link.sort_order").Joins("JOIN crm_opportunity_catalog_items AS item ON item.tenant_id=link.tenant_id AND item.id=link.catalog_item_id").Where("link.tenant_id=? AND link.opportunity_id IN ?", tenantID, ids).Order("link.opportunity_id,link.kind,link.sort_order,item.id").Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, item := range rows {
		index, ok := positions[item.OpportunityID]
		if !ok {
			continue
		}
		value := OpportunityCatalogValue{ID: item.CatalogItemID, Code: item.Code, Name: item.Name, Enabled: item.Enabled}
		if item.Kind == CatalogKindType {
			values[index].Types = append(values[index].Types, value)
		} else {
			values[index].Sources = append(values[index].Sources, value)
		}
	}
	for index := range values {
		if len(values[index].Types) > 0 {
			values[index].Type = joinOpportunityCatalogValueNames(values[index].Types)
		}
		if len(values[index].Sources) > 0 {
			values[index].Source = joinOpportunityCatalogValueNames(values[index].Sources)
		}
	}
	return nil
}

func joinOpportunityCatalogValueNames(values []OpportunityCatalogValue) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name)
	}
	return strings.Join(names, "、")
}

func splitCatalogText(value string) []string {
	result, seen := []string{}, map[string]bool{}
	for _, part := range strings.Split(value, "、") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, name)
	}
	return result
}

func normalizedCatalogNames(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		_, normalized, _ := normalizeCatalogName(value)
		result = append(result, normalized)
	}
	return result
}
func uniqueCatalogIDs(values []uint64) []uint64 {
	result := make([]uint64, 0, len(values))
	seen := map[uint64]bool{}
	for _, value := range values {
		if value == 0 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
func joinCatalogNames(values []CatalogItem) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name)
	}
	return strings.Join(names, "、")
}
