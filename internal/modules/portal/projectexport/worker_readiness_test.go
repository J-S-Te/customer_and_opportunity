package projectexport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
)

type exportReadinessStub struct {
	ready bool
	err   error
	calls int
}

func (s *exportReadinessStub) HasFreshHeartbeat(context.Context, string, time.Time) (bool, error) {
	s.calls++
	return s.ready, s.err
}

func TestCreateFailsClosedWithoutRendererButAllowsExistingReplay(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC)
	actor := Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}
	readiness := &exportReadinessStub{}
	repo := &repoStub{findErr: ErrNotFound}
	projects := &projectStub{detail: &project.Detail{Snapshot: project.Snapshot{ProjectID: "P/1", CustomerID: 7, SourceUpdatedAt: now}}}
	projects.detail.Snapshot.TenantID = "t"
	service := NewService(repo, projects, testClock{now}, &testIDs{"new"}, time.Minute).UseWorkerReadiness(readiness, 30*time.Second)
	if value, err := service.Create(context.Background(), actor, "P/1", "idem"); !errors.Is(err, ErrWorkerUnavailable) || value.PublicID != "" || readiness.calls != 1 || projects.calls != 0 {
		t.Fatalf("value=%+v err=%v readiness_calls=%d project_calls=%d", value, err, readiness.calls, projects.calls)
	}
	repo.find = &Job{PublicID: "existing", TenantID: "t", CustomerID: 7, AccountID: "account", ProjectID: "P/1", IdempotencyKey: "idem", RequestHash: digest("P/1")}
	repo.findErr = nil
	if value, err := service.Create(context.Background(), actor, "P/1", "idem"); err != nil || value.PublicID != "existing" || readiness.calls != 1 {
		t.Fatalf("replay=%+v err=%v readiness_calls=%d", value, err, readiness.calls)
	}
}

func TestCreateFailsClosedOnRendererReadinessQueryError(t *testing.T) {
	readiness := &exportReadinessStub{err: errors.New("database unavailable")}
	service := NewService(&repoStub{findErr: ErrNotFound}, &projectStub{}, testClock{time.Now()}, &testIDs{"id"}, time.Minute).UseWorkerReadiness(readiness, time.Minute)
	_, err := service.Create(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "P/1", "key")
	if !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
