package opportunity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type signedContractCountRepository struct {
	Repository
	listItems  []Response
	boardItems []Response
	detail     *Opportunity
}

func (r *signedContractCountRepository) List(context.Context, auth.Principal, ListQuery) (pagination.Page[Response], error) {
	return pagination.Page[Response]{Items: append([]Response(nil), r.listItems...), Page: 1, PageSize: 100, Total: int64(len(r.listItems))}, nil
}

func (r *signedContractCountRepository) Board(context.Context, auth.Principal, ListQuery) ([]Response, error) {
	return append([]Response(nil), r.boardItems...), nil
}

func (r *signedContractCountRepository) FindByID(context.Context, auth.Principal, uint64) (*Opportunity, error) {
	return r.detail, nil
}

func (r *signedContractCountRepository) ListMembers(context.Context, string, uint64, bool) ([]Member, error) {
	return nil, nil
}

type signedContractCounterStub struct {
	counts map[uint64]uint64
	err    error
	calls  [][]uint64
}

func (s *signedContractCounterStub) CountSignedContracts(_ context.Context, ids []uint64) (map[uint64]uint64, error) {
	s.calls = append(s.calls, append([]uint64(nil), ids...))
	return s.counts, s.err
}

func TestOpportunityListAndBoardEnrichSignedContractCountsInOneBatch(t *testing.T) {
	repo := &signedContractCountRepository{
		listItems:  []Response{{ID: 7}, {ID: 9}},
		boardItems: []Response{{ID: 7, CurrentStage: StageInitial}, {ID: 9, CurrentStage: StageSigned}},
	}
	counter := &signedContractCounterStub{counts: map[uint64]uint64{7: 2, 9: 0}}
	service := NewService(nil, repo, nil, nil).UseSignedContractCounter(counter)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})

	page, err := service.List(ctx, ListQuery{Page: 1, PageSize: 100})
	if err != nil || len(counter.calls) != 1 || len(counter.calls[0]) != 2 || page.Items[0].SignedContractCount == nil || *page.Items[0].SignedContractCount != 2 || page.Items[1].SignedContractCount == nil || *page.Items[1].SignedContractCount != 0 {
		t.Fatalf("page=%#v calls=%#v err=%v", page, counter.calls, err)
	}
	columns, err := service.Board(ctx, ListQuery{})
	if err != nil || len(counter.calls) != 2 || len(counter.calls[1]) != 2 || columns[0].Items[0].SignedContractCount == nil || *columns[0].Items[0].SignedContractCount != 2 {
		t.Fatalf("columns=%#v calls=%#v err=%v", columns, counter.calls, err)
	}
}

func TestOpportunityBoardBatchesAtMostOneThousandIDs(t *testing.T) {
	items := make([]Response, 1000)
	counts := make(map[uint64]uint64, len(items))
	for index := range items {
		items[index] = Response{ID: uint64(index + 1), CurrentStage: StageInitial}
		counts[uint64(index+1)] = uint64(index % 3)
	}
	repo := &signedContractCountRepository{boardItems: items}
	counter := &signedContractCounterStub{counts: counts}
	service := NewService(nil, repo, nil, nil).UseSignedContractCounter(counter)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	if _, err := service.Board(ctx, ListQuery{}); err != nil || len(counter.calls) != 1 || len(counter.calls[0]) != 1000 {
		t.Fatalf("calls=%d batch=%d err=%v", len(counter.calls), len(counter.calls[0]), err)
	}
}

func TestOpportunityDetailQueriesOneCountAndDependencyFailureStaysNullable(t *testing.T) {
	repo := &signedContractCountRepository{detail: &Opportunity{Model: database.Model{ID: 7, TenantID: "tenant-a"}}}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	counter := &signedContractCounterStub{counts: map[uint64]uint64{7: 3}}
	service := NewService(nil, repo, nil, nil).UseSignedContractCounter(counter)
	result, err := service.Get(ctx, 7)
	if err != nil || len(counter.calls) != 1 || len(counter.calls[0]) != 1 || result.SignedContractCount == nil || *result.SignedContractCount != 3 {
		t.Fatalf("result=%#v calls=%#v err=%v", result, counter.calls, err)
	}

	counter = &signedContractCounterStub{err: errors.New("contract unavailable")}
	result, err = NewService(nil, repo, nil, nil).UseSignedContractCounter(counter).Get(ctx, 7)
	if err != nil || result.SignedContractCount != nil {
		t.Fatalf("dependency failure result=%#v err=%v", result, err)
	}
}

func TestSignedContractCountJSONDistinguishesZeroAndUnavailable(t *testing.T) {
	zero := uint64(0)
	available, err := json.Marshal(Response{ID: 7, SignedContractCount: &zero})
	if err != nil || !strings.Contains(string(available), `"signed_contract_count":0`) {
		t.Fatalf("available JSON=%s err=%v", available, err)
	}
	unavailable, err := json.Marshal(Response{ID: 7})
	if err != nil || !strings.Contains(string(unavailable), `"signed_contract_count":null`) {
		t.Fatalf("unavailable JSON=%s err=%v", unavailable, err)
	}
}

func TestOpportunityGetHandlerOutputsSignedContractCountNumberZeroAndNull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name     string
		counter  *signedContractCounterStub
		expected string
	}{
		{name: "number", counter: &signedContractCounterStub{counts: map[uint64]uint64{7: 4}}, expected: `"signed_contract_count":4`},
		{name: "zero", counter: &signedContractCounterStub{counts: map[uint64]uint64{7: 0}}, expected: `"signed_contract_count":0`},
		{name: "unavailable", counter: &signedContractCounterStub{err: errors.New("contract unavailable")}, expected: `"signed_contract_count":null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &signedContractCountRepository{detail: &Opportunity{Model: database.Model{ID: 7, TenantID: "tenant-a"}}}
			service := NewService(nil, repo, nil, nil).UseSignedContractCounter(tt.counter)
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Params = gin.Params{{Key: "id", Value: "7"}}
			request := httptest.NewRequest(http.MethodGet, "/opportunities/7", nil)
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}))
			ginContext.Request = request
			NewHandler(service).Get(ginContext)
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), tt.expected) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
