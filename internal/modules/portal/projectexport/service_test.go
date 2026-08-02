package projectexport

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
)

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time { return c.now }

type testIDs struct{ value string }

func (i *testIDs) NewID() string { return i.value }

type projectStub struct {
	detail *project.Detail
	calls  int
}

func (p *projectStub) Get(_ context.Context, scope project.Scope, id string) (*project.Detail, error) {
	p.calls++
	if scope.TenantID != "t" || scope.CustomerID != 7 || id != "P/1" {
		return nil, project.ErrNotFound
	}
	return p.detail, nil
}

type repoStub struct {
	find              *Job
	findErr           error
	created           *Job
	createErr         error
	grant             *Grant
	authorized        *Job
	grantID           uint64
	consumed          bool
	completionSuccess bool
	consumeErr        error
	mu                sync.Mutex
}

func (r *repoStub) FindByKey(context.Context, Actor, string) (*Job, error) { return r.find, r.findErr }
func (r *repoStub) Create(_ context.Context, job *Job, _ *Event) error {
	r.created = job
	return r.createErr
}
func (r *repoStub) FindOwned(context.Context, Actor, string, bool) (*Job, error) {
	if r.authorized == nil {
		return nil, ErrNotFound
	}
	return r.authorized, nil
}
func (r *repoStub) CreateGrant(_ context.Context, _ Actor, _ string, id string, _, expires time.Time, hash string) (*Grant, error) {
	r.grant = &Grant{PublicID: id, ExpiresAt: expires, TokenHash: hash}
	return r.grant, nil
}
func (r *repoStub) ConsumeGrant(context.Context, Actor, string, string, time.Time, string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.consumeErr != nil {
		return nil, r.consumeErr
	}
	if r.consumed {
		return nil, ErrInvalidGrant
	}
	r.consumed = true
	return r.authorized, nil
}

func TestConcurrentDownloadAllowsOnlyOneConsumer(t *testing.T) {
	pdf := []byte("%PDF-1.7\nvalid")
	repo := &repoStub{authorized: &Job{ID: 9, Status: StatusReady, FileName: "p.pdf", FileSize: int64(len(pdf)), FileBytes: pdf, FileHash: digestBytes(pdf)}}
	service := NewService(repo, nil, testClock{time.Now()}, nil, 0)
	actor := Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}
	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			_, err := service.Download(context.Background(), actor, "export", strings.Repeat("a", 64))
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInvalidGrant):
			rejected++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
}
func (r *repoStub) RecordDeliveryOutcome(_ context.Context, _ Actor, _ uint64, _ time.Time, _ string, success bool, _ string) error {
	r.completionSuccess = success
	return nil
}

func TestCreateIsScopedAndDurablyIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	projects := &projectStub{detail: &project.Detail{Snapshot: project.Snapshot{ProjectID: "P/1", CustomerID: 7, SourceUpdatedAt: now}}}
	projects.detail.Snapshot.TenantID = "t"
	repo := &repoStub{findErr: ErrNotFound}
	service := NewService(repo, projects, testClock{now}, &testIDs{"export-1"}, 15*time.Minute)
	result, err := service.Create(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "P/1", "key")
	if err != nil || result.PublicID != "export-1" || repo.created.AccountID != "account" || repo.created.Status != StatusPending {
		t.Fatalf("result=%+v created=%+v err=%v", result, repo.created, err)
	}
	repo.find, repo.findErr = repo.created, nil
	if replay, err := service.Create(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "P/1", "key"); err != nil || replay.PublicID != result.PublicID || projects.calls != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, projects.calls, err)
	}
}

func TestCreateFailsClosedOnRepositoryReadError(t *testing.T) {
	want := errors.New("database unavailable")
	projects := &projectStub{}
	service := NewService(&repoStub{findErr: want}, projects, testClock{}, &testIDs{"id"}, 0)
	_, err := service.Create(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "P/1", "key")
	if !errors.Is(err, want) || projects.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, projects.calls)
	}
}

func TestDownloadAtomicallyConsumesGrantBeforeTransfer(t *testing.T) {
	pdf := []byte("%PDF-1.7\nvalid")
	job := &Job{ID: 9, Status: StatusReady, FileName: "p.pdf", FileSize: int64(len(pdf)), FileBytes: pdf, FileHash: digestBytes(pdf)}
	repo := &repoStub{authorized: job, grantID: 11}
	service := NewService(repo, nil, testClock{time.Now()}, nil, 0)
	download, err := service.Download(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "export", strings.Repeat("a", 64))
	if err != nil || !repo.consumed {
		t.Fatalf("grant not consumed before transfer: consumed=%v err=%v", repo.consumed, err)
	}
	if _, replayErr := service.Download(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "export", strings.Repeat("a", 64)); !errors.Is(replayErr, ErrInvalidGrant) {
		t.Fatalf("consumed token replay err=%v", replayErr)
	}
	if err = download.Complete(context.Background(), true, ""); err != nil || !repo.consumed || !repo.completionSuccess {
		t.Fatalf("completion consumed=%v err=%v", repo.consumed, err)
	}
	repo.consumed = false
	job.FileHash = strings.Repeat("0", 64)
	if _, err := service.Download(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "export", strings.Repeat("b", 64)); !errors.Is(err, ErrNotReady) || !repo.consumed {
		t.Fatalf("consumed=%v err=%v", repo.consumed, err)
	}
}

func TestDownloadStreamFailureKeepsAtMostOnceGrantConsumed(t *testing.T) {
	pdf := []byte("%PDF-1.7\nvalid")
	job := &Job{ID: 9, Status: StatusReady, FileName: "p.pdf", FileSize: int64(len(pdf)), FileBytes: pdf, FileHash: digestBytes(pdf)}
	repo := &repoStub{authorized: job, grantID: 11}
	service := NewService(repo, nil, testClock{time.Now()}, nil, 0)
	download, err := service.Download(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "export", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err = download.Complete(context.Background(), false, "STREAM_INCOMPLETE"); err != nil || !repo.consumed || repo.completionSuccess {
		t.Fatalf("failed transfer must remain consumed: consumed=%v delivery_success=%v err=%v", repo.consumed, repo.completionSuccess, err)
	}
}

func TestCreatePreservesOpaqueProjectID(t *testing.T) {
	now := time.Now().UTC()
	id := " P/1 "
	projects := &projectStub{detail: &project.Detail{Snapshot: project.Snapshot{ProjectID: id, CustomerID: 7, SourceUpdatedAt: now}}}
	projects.detail.Snapshot.TenantID = "t"
	repo := &repoStub{findErr: ErrNotFound}
	// Use a reader that explicitly accepts the whitespace-bearing opaque ID.
	reader := projectReaderFunc(func(_ context.Context, scope project.Scope, got string) (*project.Detail, error) {
		if scope.TenantID != "t" || scope.CustomerID != 7 || got != id {
			return nil, project.ErrNotFound
		}
		return projects.detail, nil
	})
	service := NewService(repo, reader, testClock{now}, &testIDs{"export"}, 0)
	if _, err := service.Create(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, id, "key"); err != nil || repo.created.ProjectID != id {
		t.Fatalf("opaque id changed: created=%q err=%v", repo.created.ProjectID, err)
	}
}

type projectReaderFunc func(context.Context, project.Scope, string) (*project.Detail, error)

func (f projectReaderFunc) Get(ctx context.Context, scope project.Scope, id string) (*project.Detail, error) {
	return f(ctx, scope, id)
}

func TestGrantUsesSingleNowAndFifteenMinuteTTL(t *testing.T) {
	now := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	repo := &repoStub{authorized: &Job{Status: StatusReady}}
	service := NewService(repo, nil, testClock{now}, &testIDs{"grant-1"}, 15*time.Minute)
	result, err := service.CreateGrant(context.Background(), Actor{TenantID: "t", CustomerID: 7, AccountID: "account"}, "export")
	if err != nil || result.GrantID != "grant-1" || !result.ExpiresAt.Equal(now.Add(15*time.Minute)) || repo.grant.TokenHash == "" || strings.Contains(repo.grant.TokenHash, result.DownloadToken) {
		t.Fatalf("result=%+v grant=%+v err=%v", result, repo.grant, err)
	}
}
