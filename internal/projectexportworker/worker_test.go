package projectexportworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectexport"
)

type workerStoreStub struct {
	job       *projectexport.Job
	completed bool
	failed    string
}

func (s *workerStoreStub) Claim(context.Context, string, time.Time, time.Duration) (*projectexport.Job, error) {
	value := s.job
	s.job = nil
	return value, nil
}
func (s *workerStoreStub) Complete(_ context.Context, _ *projectexport.Job, _, _ string, _ []byte, _ string, _ time.Time) error {
	s.completed = true
	return nil
}
func (s *workerStoreStub) Fail(_ context.Context, _ *projectexport.Job, _, code string, _ time.Time) error {
	s.failed = code
	return nil
}

type rendererStub struct {
	value []byte
	err   error
}

type leaseStoreStub struct {
	job         projectexport.Job
	claimedBy   string
	completedBy string
}

func (s *leaseStoreStub) Claim(_ context.Context, worker string, now time.Time, lease time.Duration) (*projectexport.Job, error) {
	if s.job.Status == projectexport.StatusPending || (s.job.Status == projectexport.StatusGenerating && s.job.LockedUntil != nil && s.job.LockedUntil.Before(now)) {
		s.job.Status, s.job.LockedBy = projectexport.StatusGenerating, worker
		until := now.Add(lease)
		s.job.LockedUntil = &until
		s.job.Version++
		copy := s.job
		s.claimedBy = worker
		return &copy, nil
	}
	return nil, nil
}
func (s *leaseStoreStub) Complete(_ context.Context, job *projectexport.Job, worker, _ string, _ []byte, _ string, now time.Time) error {
	if s.job.Version != job.Version || s.job.LockedBy != worker || s.job.LockedUntil == nil || !s.job.LockedUntil.After(now) {
		return errLeaseLost
	}
	s.completedBy = worker
	return nil
}
func (s *leaseStoreStub) Fail(context.Context, *projectexport.Job, string, string, time.Time) error {
	return nil
}

func (r rendererStub) Render(context.Context, project.Detail, time.Time) ([]byte, error) {
	return r.value, r.err
}

func TestWorkerRendersOnlyMatchingImmutableSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	detail := project.Detail{Snapshot: project.Snapshot{ProjectID: "P-1", CustomerID: 7, SourceUpdatedAt: now}}
	detail.Snapshot.TenantID = "t"
	payload, _ := json.Marshal(projectexport.Capture{TenantID: "t", CustomerID: 7, Detail: detail})
	store := &workerStoreStub{job: &projectexport.Job{TenantID: "t", CustomerID: 7, ProjectID: "P-1", SourceUpdatedAt: now, SnapshotJSON: payload}}
	worker := NewWorker(store, rendererStub{value: []byte("%PDF-1.7\nreal")}, "w", time.Second, time.Minute)
	worker.now = func() time.Time { return now }
	if worked, err := worker.RunOnce(context.Background()); !worked || err != nil || !store.completed {
		t.Fatalf("worked=%v complete=%v err=%v", worked, store.completed, err)
	}
}

func TestWorkerFailsClosedOnRendererError(t *testing.T) {
	now := time.Now().UTC()
	detail := project.Detail{Snapshot: project.Snapshot{ProjectID: "P", CustomerID: 1, SourceUpdatedAt: now}}
	detail.Snapshot.TenantID = "t"
	payload, _ := json.Marshal(projectexport.Capture{TenantID: "t", CustomerID: 1, Detail: detail})
	store := &workerStoreStub{job: &projectexport.Job{TenantID: "t", CustomerID: 1, ProjectID: "P", SourceUpdatedAt: now, SnapshotJSON: payload}}
	worker := NewWorker(store, rendererStub{err: errors.New("renderer unavailable")}, "w", time.Second, time.Minute)
	if worked, err := worker.RunOnce(context.Background()); !worked || err == nil || store.completed || store.failed != "PORTAL_PROJECT_EXPORT_RENDER_FAILED" {
		t.Fatalf("worked=%v complete=%v failed=%s err=%v", worked, store.completed, store.failed, err)
	}
}

func TestExpiredGeneratingLeaseCanBeTakenOverAndOldWorkerCannotComplete(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Second)
	store := &leaseStoreStub{job: projectexport.Job{ID: 1, Status: projectexport.StatusGenerating, LockedBy: "old", LockedUntil: &expired, Version: 2}}
	claimed, err := store.Claim(context.Background(), "new", now, time.Minute)
	if err != nil || claimed == nil || claimed.LockedBy != "new" || claimed.Version != 3 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	old := *claimed
	old.LockedBy, old.Version = "old", 2
	if err := store.Complete(context.Background(), &old, "old", "x.pdf", []byte("%PDF"), "hash", now); !errors.Is(err, errLeaseLost) {
		t.Fatalf("old worker err=%v", err)
	}
	if err := store.Complete(context.Background(), claimed, "new", "x.pdf", []byte("%PDF"), "hash", now); err != nil || store.completedBy != "new" {
		t.Fatalf("new worker complete=%q err=%v", store.completedBy, err)
	}
}

func TestPDFRowsPaginateWithoutDroppingLongContent(t *testing.T) {
	long := strings.Repeat("项目进度ABC", 80)
	pages := paginatePDFRows([]string{long, "末行"}, 20, 4)
	if len(pages) < 2 {
		t.Fatalf("pages=%d, want multiple pages", len(pages))
	}
	joined := strings.Join(flattenPDFPages(pages), "")
	if joined != long+"末行" {
		t.Fatalf("pagination lost content: got %d runes, want %d", len([]rune(joined)), len([]rune(long+"末行")))
	}
	for _, page := range pages {
		if len(page) > 4 {
			t.Fatalf("page has %d lines", len(page))
		}
		for _, line := range page {
			if runewidth.StringWidth(line) > 20 {
				t.Fatalf("line width=%d: %q", runewidth.StringWidth(line), line)
			}
		}
	}
}

func flattenPDFPages(pages [][]string) []string {
	var result []string
	for _, page := range pages {
		result = append(result, page...)
	}
	return result
}
