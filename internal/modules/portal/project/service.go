package project

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

var ErrNotFound = apperror.New(http.StatusNotFound, "PORTAL_PROJECT_NOT_FOUND", "project not found")

type Scope struct {
	TenantID   string
	CustomerID uint64
}
type ListQuery struct {
	Status         string
	Page, PageSize int
}

// HistoryItem 是返回 CRM 的最小项目投影，刻意排除 Portal 账号、负责人联系方式、团队成员和源版本细节。
type HistoryItem struct {
	ProjectID       string     `json:"project_id"`
	ProjectName     string     `json:"project_name"`
	ContractNo      string     `json:"contract_no"`
	Status          string     `json:"status"`
	ProgressPct     uint8      `json:"progress_pct"`
	CurrentStage    string     `json:"current_stage"`
	ExpectedEndDate *time.Time `json:"expected_end_date,omitempty"`
	Delayed         bool       `json:"delayed"`
	SourceUpdatedAt time.Time  `json:"source_updated_at"`
	SyncedAt        time.Time  `json:"synced_at"`
	// SyncLastSuccessAt 描述客户级同步链路，SyncedAt 则描述当前快照行何时发生变化。
	SyncLastSuccessAt *time.Time `json:"sync_last_success_at"`
	Stale             bool       `json:"stale"`
	// 客户至少成功同步一次后才计算 StalenessSeconds，避免零值伪装成“刚同步”。
	StalenessSeconds *int64 `json:"staleness_seconds"`
}

type HistoryPage struct {
	Items    []HistoryItem `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int64         `json:"total"`
}

type Repository interface {
	List(context.Context, Scope, ListQuery) (pagination.Page[Snapshot], error)
	LastSuccessfulSync(context.Context, Scope) (*time.Time, error)
	Find(context.Context, Scope, string) (*Detail, error)
	AssertVisible(context.Context, Scope, string) error
	FindStatusForEvaluation(context.Context, Scope, string) (string, error)
	ListActivities(context.Context, Scope, string, int, int) (pagination.Page[Activity], error)
	UpsertBundle(context.Context, *Bundle) (bool, error)
}

type Source interface {
	ChangedProjects(context.Context, string, uint64, string) ([]Bundle, error)
}

type Service struct {
	repo   Repository
	source Source
}

func NewService(repo Repository, source Source) *Service { return &Service{repo: repo, source: source} }

func (s *Service) List(ctx context.Context, scope Scope, query ListQuery) (pagination.Page[Snapshot], error) {
	query.Page, query.PageSize = pagination.Normalize(query.Page, query.PageSize)
	return s.repo.List(ctx, scope, query)
}

// History 只读取 Portal 已持久化投影；新鲜度表示本地上次成功同步时间，不承诺上游当前可用。
func (s *Service) History(ctx context.Context, scope Scope, query ListQuery, now time.Time, staleAfter time.Duration) (HistoryPage, error) {
	if strings.TrimSpace(scope.TenantID) == "" || scope.CustomerID == 0 || query.Page < 1 || query.PageSize < 1 || query.PageSize > pagination.MaxPageSize || staleAfter <= 0 {
		return HistoryPage{}, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request")
	}
	page, err := s.repo.List(ctx, scope, query)
	if err != nil {
		return HistoryPage{}, err
	}
	lastSuccessAt, err := s.repo.LastSuccessfulSync(ctx, scope)
	if err != nil {
		return HistoryPage{}, err
	}
	var normalizedLastSuccess *time.Time
	var stalenessSeconds *int64
	stale := true
	if lastSuccessAt != nil && !lastSuccessAt.IsZero() {
		lastSuccess := lastSuccessAt.UTC()
		normalizedLastSuccess = &lastSuccess
		age := now.UTC().Sub(lastSuccess)
		if age < 0 {
			age = 0
		}
		seconds := int64(age / time.Second)
		stalenessSeconds = &seconds
		stale = age > staleAfter
	}
	result := HistoryPage{Items: make([]HistoryItem, 0, len(page.Items)), Page: page.Page, PageSize: page.PageSize, Total: page.Total}
	for i := range page.Items {
		value := &page.Items[i]
		result.Items = append(result.Items, HistoryItem{
			ProjectID: value.ProjectID, ProjectName: value.ProjectName, ContractNo: value.ContractNo,
			Status: value.Status, ProgressPct: value.ProgressPct, CurrentStage: value.CurrentStage,
			ExpectedEndDate: value.ExpectedEndDate, Delayed: value.Delayed,
			SourceUpdatedAt: value.SourceUpdatedAt.UTC(), SyncedAt: value.SyncedAt.UTC(),
			SyncLastSuccessAt: normalizedLastSuccess, Stale: stale, StalenessSeconds: stalenessSeconds,
		})
	}
	return result, nil
}
func (s *Service) Get(ctx context.Context, scope Scope, projectID string) (*Detail, error) {
	if strings.TrimSpace(projectID) == "" || scope.CustomerID == 0 {
		return nil, ErrNotFound
	}
	return s.repo.Find(ctx, scope, projectID)
}

// StatusForEvaluation 在评价提交事务中锁定项目快照，防止并发源同步在资格验证与评价插入之间改变完成状态。
func (s *Service) StatusForEvaluation(ctx context.Context, scope Scope, projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" || scope.CustomerID == 0 {
		return "", ErrNotFound
	}
	return s.repo.FindStatusForEvaluation(ctx, scope, projectID)
}
func (s *Service) Activities(ctx context.Context, scope Scope, projectID string, page, pageSize int) (pagination.Page[Activity], error) {
	if strings.TrimSpace(projectID) == "" {
		return pagination.Page[Activity]{}, ErrNotFound
	}
	if scope.CustomerID == 0 {
		return pagination.Page[Activity]{}, ErrNotFound
	}
	if err := s.repo.AssertVisible(ctx, scope, projectID); err != nil {
		return pagination.Page[Activity]{}, err
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	return s.repo.ListActivities(ctx, scope, projectID, page, pageSize)
}

// Sync 只接受源系统拥有的快照，并阻止旧源版本覆盖更新的客户可见数据。
func (s *Service) Sync(ctx context.Context, tenantID string, customerID uint64, cursor string) (int, error) {
	bundles, err := s.source.ChangedProjects(ctx, tenantID, customerID, cursor)
	if err != nil {
		return 0, err
	}
	updated := 0
	for i := range bundles {
		if bundles[i].Snapshot.TenantID != tenantID || bundles[i].Snapshot.CustomerID != customerID {
			continue
		}
		changed, err := s.repo.UpsertBundle(ctx, &bundles[i])
		if err != nil {
			return updated, err
		}
		if changed {
			updated++
		}
	}
	return updated, nil
}
