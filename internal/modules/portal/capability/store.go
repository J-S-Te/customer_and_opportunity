package capability

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Reader 读取某客户的服务项；缺失配置行返回默认全部开通。
type Reader interface {
	Get(context.Context, string, uint64) (Options, error)
}

// Writer 写入某客户的服务项。
type Writer interface {
	Upsert(context.Context, string, uint64, Options) (Options, error)
}

// Store 同时提供读取与配置能力，供机器管理接口和会话判权复用。
type Store interface {
	Reader
	Writer
}

type optionRow struct {
	TenantID         string    `gorm:"column:tenant_id;primaryKey;size:64"`
	CustomerID       uint64    `gorm:"column:customer_id;primaryKey"`
	CapabilitiesJSON string    `gorm:"column:capabilities_json;type:json;not null"`
	Version          uint64    `gorm:"column:version;not null;default:1"`
	CreatedAt        time.Time `gorm:"column:created_at;precision:3;not null"`
	UpdatedAt        time.Time `gorm:"column:updated_at;precision:3;not null"`
}

func (optionRow) TableName() string { return "portal_customer_service_options" }

type GORMStore struct {
	db  *gorm.DB
	now func() time.Time
}

func NewGORMStore(db *gorm.DB) *GORMStore {
	return &GORMStore{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (s *GORMStore) Get(ctx context.Context, tenantID string, customerID uint64) (Options, error) {
	var row optionRow
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND customer_id = ?", tenantID, customerID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DefaultOptions(), nil
	}
	if err != nil {
		return Options{}, err
	}
	var values map[string]bool
	if err := json.Unmarshal([]byte(row.CapabilitiesJSON), &values); err != nil {
		return Options{}, err
	}
	return OptionsFromMap(values), nil
}

func (s *GORMStore) Upsert(ctx context.Context, tenantID string, customerID uint64, options Options) (Options, error) {
	now := s.now()
	payload, err := json.Marshal(options.ToMap())
	if err != nil {
		return Options{}, err
	}
	row := optionRow{
		TenantID: tenantID, CustomerID: customerID, CapabilitiesJSON: string(payload),
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "customer_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"capabilities_json": row.CapabilitiesJSON,
			"version":           gorm.Expr("version + 1"),
			"updated_at":        now,
		}),
	}).Create(&row).Error
	if err != nil {
		return Options{}, err
	}
	return options, nil
}

type cacheKey struct {
	tenantID   string
	customerID uint64
}

type cacheEntry struct {
	options   Options
	expiresAt time.Time
}

// CachedReader 以短 TTL 缓存服务项，避免每次门户请求都查询数据库；
// 配置变更最迟在 TTL 后生效，属于短期模型的预期延迟。
type CachedReader struct {
	store Reader
	ttl   time.Duration
	now   func() time.Time
	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

func NewCachedReader(store Reader, ttl time.Duration) *CachedReader {
	return &CachedReader{store: store, ttl: ttl, now: time.Now, cache: make(map[cacheKey]cacheEntry)}
}

func (c *CachedReader) Get(ctx context.Context, tenantID string, customerID uint64) (Options, error) {
	key := cacheKey{tenantID: tenantID, customerID: customerID}
	now := c.now().UTC()
	c.mu.Lock()
	if entry, ok := c.cache[key]; ok && entry.expiresAt.After(now) {
		options := entry.options
		c.mu.Unlock()
		return options, nil
	}
	c.mu.Unlock()
	options, err := c.store.Get(ctx, tenantID, customerID)
	if err != nil {
		return Options{}, err
	}
	c.mu.Lock()
	c.cache[key] = cacheEntry{options: options, expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return options, nil
}
