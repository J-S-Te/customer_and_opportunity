package report

import (
	"context"
	"errors"
	"testing"
	"time"
)

type reportReadinessStub struct {
	ready bool
	err   error
	calls int
}

func (s *reportReadinessStub) HasFreshHeartbeat(context.Context, string, time.Time) (bool, error) {
	s.calls++
	return s.ready, s.err
}

func TestCreateFailsClosedWithoutDeliveryWorkerButAllowsExistingReplay(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC)
	actor := Actor{TenantID: "tenant", CustomerID: 9, AccountID: "account"}
	cmd := CreateCommand{ProjectID: "P-1", ReportType: "CHECK", Reason: "reason", ReceiveEmail: "a@example.com", IdempotencyKey: "idem"}
	readiness := &reportReadinessStub{}
	repo := &reportRepoStub{}
	service := NewService(repo, &projectAccessStub{allowed: true}, emailProtectorStub{}, nil, reportClock{now: now}, idStub{value: "request-no"}).
		UseWorkerReadiness(readiness, 30*time.Second)
	if value, err := service.Create(context.Background(), actor, cmd); value != nil || !errors.Is(err, ErrDeliveryUnavailable) || readiness.calls != 1 {
		t.Fatalf("value=%+v err=%v readiness_calls=%d", value, err, readiness.calls)
	}
	hash := requestHash(cmd)
	repo.findByKey = &Request{ActorModel: ActorModel{TenantID: actor.TenantID}, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ProjectID: cmd.ProjectID, ReportType: cmd.ReportType, Reason: cmd.Reason, IdempotencyKey: cmd.IdempotencyKey, RequestHash: hash}
	if value, err := service.Create(context.Background(), actor, cmd); err != nil || value != repo.findByKey || readiness.calls != 1 {
		t.Fatalf("replay=%+v err=%v readiness_calls=%d", value, err, readiness.calls)
	}
}

func TestCreateFailsClosedOnWorkerReadinessQueryError(t *testing.T) {
	readiness := &reportReadinessStub{err: errors.New("database unavailable")}
	service := NewService(&reportRepoStub{}, &projectAccessStub{allowed: true}, emailProtectorStub{}, nil, reportClock{now: time.Now()}, idStub{value: "id"}).
		UseWorkerReadiness(readiness, 30*time.Second)
	_, err := service.Create(context.Background(), Actor{TenantID: "tenant", CustomerID: 9, AccountID: "account"}, CreateCommand{ProjectID: "P", ReportType: "R", Reason: "reason", ReceiveEmail: "a@example.com", IdempotencyKey: "key"})
	if !errors.Is(err, ErrDeliveryUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
