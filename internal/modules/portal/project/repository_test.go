package project

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestEvaluationStatusQueryLocksScopedSnapshotRow(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "user:pass@tcp(127.0.0.1:1)/portal?parseTime=true", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	statement := evaluationStatusQuery(db, Scope{TenantID: "tenant-a", CustomerID: 7}, "P-1").Take(&Snapshot{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"tenant_id = ?", "customer_id = ?", "project_id = ?", "FOR SHARE"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("locked evaluation status SQL missing %q: %s", fragment, sql)
		}
	}
}

func TestVisibleSnapshotQueryAlwaysScopesTenantCustomerAndProject(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "user:pass@tcp(127.0.0.1:1)/portal?parseTime=true", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	statement := visibleSnapshotQuery(db, Scope{TenantID: "tenant-a", CustomerID: 7}, "P-1").Take(&Snapshot{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"tenant_id = ?", "customer_id = ?", "project_id = ?", "deleted_at"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("project visibility SQL missing %q: %s", fragment, sql)
		}
	}
}

func TestSuccessfulSyncQueryAlwaysScopesTenantAndCustomer(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "user:pass@tcp(127.0.0.1:1)/portal?parseTime=true", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	statement := successfulSyncQuery(db, Scope{TenantID: "tenant-a", CustomerID: 7}).Take(&projectSyncState{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"portal_project_sync_states", "tenant_id = ?", "customer_id = ?"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("successful sync SQL missing %q: %s", fragment, sql)
		}
	}
	if len(statement.Vars) < 2 || statement.Vars[0] != "tenant-a" || statement.Vars[1] != uint64(7) {
		t.Fatalf("successful sync scope variables=%#v", statement.Vars)
	}
}

func TestActivitiesProvesParentVisibilityBeforePagedQuery(t *testing.T) {
	repo := &activityRepository{}
	service := NewService(repo, nil)
	page, err := service.Activities(context.Background(), Scope{TenantID: "tenant-a", CustomerID: 7}, "P-1", 2, 20)
	if err != nil || page.Page != 2 || page.PageSize != 20 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if strings.Join(repo.calls, ",") != "visible,activities" {
		t.Fatalf("unexpected repository call order: %v", repo.calls)
	}
}

func TestActivitiesDoesNotQueryChildrenWhenParentIsNotVisible(t *testing.T) {
	repo := &activityRepository{visibilityErr: ErrNotFound}
	service := NewService(repo, nil)
	if _, err := service.Activities(context.Background(), Scope{TenantID: "tenant-a", CustomerID: 7}, "foreign", 1, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if strings.Join(repo.calls, ",") != "visible" {
		t.Fatalf("child query ran before visibility proof: %v", repo.calls)
	}
}

func TestNormalizeChildrenPinsTenantCustomerAndProject(t *testing.T) {
	bundle := Bundle{
		Snapshot:   Snapshot{ProjectID: "project-a", CustomerID: 7},
		Milestones: []Milestone{{TenantID: "evil", CustomerID: 9, ProjectID: "other"}},
		Activities: []Activity{{TenantID: "evil", CustomerID: 9, ProjectID: "other"}},
		Team:       []TeamMember{{TenantID: "evil", CustomerID: 9, ProjectID: "other"}},
	}
	bundle.Snapshot.TenantID = "tenant-a"
	normalizeChildren(&bundle)
	for _, scope := range []struct {
		tenant   string
		customer uint64
		project  string
	}{{bundle.Milestones[0].TenantID, bundle.Milestones[0].CustomerID, bundle.Milestones[0].ProjectID}, {bundle.Activities[0].TenantID, bundle.Activities[0].CustomerID, bundle.Activities[0].ProjectID}, {bundle.Team[0].TenantID, bundle.Team[0].CustomerID, bundle.Team[0].ProjectID}} {
		if scope.tenant != "tenant-a" || scope.customer != 7 || scope.project != "project-a" {
			t.Fatalf("child scope was not pinned: %#v", scope)
		}
	}
}

func TestSourceOrderingRejectsReplayAndOutOfOrderSnapshot(t *testing.T) {
	current := time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	if isNewerSource(current, current) || isNewerSource(current.Add(-time.Millisecond), current) {
		t.Fatal("replay or older source timestamp was accepted")
	}
	if !isNewerSource(current.Add(time.Millisecond), current) {
		t.Fatal("newer source timestamp was rejected")
	}
}

func TestHistoryReturnsOnlyLocalProjectionWithExplicitStaleness(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	lastSuccess := now.Add(-11 * time.Minute)
	repo := &historyRepository{lastSuccessAt: &lastSuccess, page: pagination.Page[Snapshot]{Items: []Snapshot{{
		ProjectID: "P-1", ProjectName: "项目", ContractNo: "HT-1", Status: "EXECUTING",
		ProgressPct: 50, CurrentStage: "实施", ManagerName: "不应返回", ManagerContactMasked: "138****0000",
		ManagerPortalAccountID: "private-account", RawVersion: "private-version",
		SourceUpdatedAt: now.Add(-9 * time.Minute), SyncedAt: now.Add(-11 * time.Minute),
	}}, Page: 1, PageSize: 20, Total: 1}}
	value, err := NewService(repo, nil).History(context.Background(), Scope{TenantID: "tenant-a", CustomerID: 7}, ListQuery{Page: 1, PageSize: 20}, now, 10*time.Minute)
	if err != nil || len(value.Items) != 1 || !value.Items[0].Stale || value.Items[0].StalenessSeconds == nil || *value.Items[0].StalenessSeconds != 660 || value.Items[0].SyncLastSuccessAt == nil || !value.Items[0].SyncLastSuccessAt.Equal(lastSuccess) {
		t.Fatalf("history=%#v err=%v", value, err)
	}
}

func TestHistoryFreshnessUsesCustomerSyncStateWhenSnapshotContentIsUnchanged(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	lastSuccess := now.Add(-time.Minute)
	repo := &historyRepository{lastSuccessAt: &lastSuccess, page: pagination.Page[Snapshot]{Items: []Snapshot{{
		ProjectID: "P-1", ProjectName: "unchanged", Status: "EXECUTING", CurrentStage: "实施",
		SourceUpdatedAt: now.Add(-24 * time.Hour), SyncedAt: now.Add(-24 * time.Hour),
	}}, Page: 1, PageSize: 20, Total: 1}}
	value, err := NewService(repo, nil).History(context.Background(), Scope{TenantID: "tenant-a", CustomerID: 7}, ListQuery{Page: 1, PageSize: 20}, now, 10*time.Minute)
	if err != nil || len(value.Items) != 1 || value.Items[0].Stale || value.Items[0].StalenessSeconds == nil || *value.Items[0].StalenessSeconds != 60 {
		t.Fatalf("history=%#v err=%v", value, err)
	}
}

func TestHistoryWithoutSuccessfulSyncIsExplicitlyStale(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	repo := &historyRepository{page: pagination.Page[Snapshot]{Items: []Snapshot{{
		ProjectID: "P-1", ProjectName: "project", Status: "EXECUTING", CurrentStage: "实施",
		SourceUpdatedAt: now, SyncedAt: now,
	}}, Page: 1, PageSize: 20, Total: 1}}
	value, err := NewService(repo, nil).History(context.Background(), Scope{TenantID: "tenant-a", CustomerID: 7}, ListQuery{Page: 1, PageSize: 20}, now, 10*time.Minute)
	if err != nil || len(value.Items) != 1 || !value.Items[0].Stale || value.Items[0].SyncLastSuccessAt != nil || value.Items[0].StalenessSeconds != nil {
		t.Fatalf("history=%#v err=%v", value, err)
	}
}

type historyRepository struct {
	Repository
	page          pagination.Page[Snapshot]
	lastSuccessAt *time.Time
	syncScope     Scope
}

func (r *historyRepository) LastSuccessfulSync(_ context.Context, scope Scope) (*time.Time, error) {
	r.syncScope = scope
	return r.lastSuccessAt, nil
}

func (r *historyRepository) List(context.Context, Scope, ListQuery) (pagination.Page[Snapshot], error) {
	return r.page, nil
}

func TestServiceSyncRejectsCrossScopeAndRepositoryOwnsOrdering(t *testing.T) {
	now := time.Now().UTC()
	repo := &captureRepository{}
	source := sourceStub{bundles: []Bundle{
		{Snapshot: Snapshot{ProjectID: "wrong-tenant", CustomerID: 7, SourceUpdatedAt: now}},
		{Snapshot: Snapshot{ProjectID: "wrong-customer", CustomerID: 8, SourceUpdatedAt: now}},
		{Snapshot: Snapshot{ProjectID: "accepted", CustomerID: 7, SourceUpdatedAt: now}},
	}}
	source.bundles[0].Snapshot.TenantID = "other"
	source.bundles[1].Snapshot.TenantID = "tenant-a"
	source.bundles[2].Snapshot.TenantID = "tenant-a"
	service := NewService(repo, source)
	updated, err := service.Sync(context.Background(), "tenant-a", 7, "cursor")
	if err != nil || updated != 1 || len(repo.projects) != 1 || repo.projects[0] != "accepted" {
		t.Fatalf("updated=%d projects=%v err=%v", updated, repo.projects, err)
	}
}

type sourceStub struct{ bundles []Bundle }

func (s sourceStub) ChangedProjects(context.Context, string, uint64, string) ([]Bundle, error) {
	return s.bundles, nil
}

type captureRepository struct{ projects []string }

func (*captureRepository) List(context.Context, Scope, ListQuery) (pagination.Page[Snapshot], error) {
	panic("not used")
}
func (*captureRepository) LastSuccessfulSync(context.Context, Scope) (*time.Time, error) {
	panic("not used")
}
func (*captureRepository) Find(context.Context, Scope, string) (*Detail, error) { panic("not used") }
func (*captureRepository) AssertVisible(context.Context, Scope, string) error   { panic("not used") }
func (*captureRepository) FindStatusForEvaluation(context.Context, Scope, string) (string, error) {
	return "COMPLETED", nil
}
func (*captureRepository) ListActivities(context.Context, Scope, string, int, int) (pagination.Page[Activity], error) {
	panic("not used")
}
func (r *captureRepository) UpsertBundle(_ context.Context, bundle *Bundle) (bool, error) {
	r.projects = append(r.projects, bundle.Snapshot.ProjectID)
	return true, nil
}

type activityRepository struct {
	calls         []string
	visibilityErr error
}

func (*activityRepository) List(context.Context, Scope, ListQuery) (pagination.Page[Snapshot], error) {
	panic("not used")
}
func (*activityRepository) LastSuccessfulSync(context.Context, Scope) (*time.Time, error) {
	panic("not used")
}
func (*activityRepository) Find(context.Context, Scope, string) (*Detail, error) { panic("not used") }
func (r *activityRepository) AssertVisible(context.Context, Scope, string) error {
	r.calls = append(r.calls, "visible")
	return r.visibilityErr
}
func (*activityRepository) FindStatusForEvaluation(context.Context, Scope, string) (string, error) {
	panic("not used")
}
func (r *activityRepository) ListActivities(_ context.Context, _ Scope, _ string, page, pageSize int) (pagination.Page[Activity], error) {
	r.calls = append(r.calls, "activities")
	return pagination.Page[Activity]{Items: []Activity{}, Page: page, PageSize: pageSize}, nil
}
func (*activityRepository) UpsertBundle(context.Context, *Bundle) (bool, error) { panic("not used") }
