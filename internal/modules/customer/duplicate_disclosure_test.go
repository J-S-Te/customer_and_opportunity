package customer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

// duplicateDisclosureRepo 同时模拟租户级查重和调用者可见范围查重，
// 用于证明错误响应不会把不可见候选的摘要带出去。
type duplicateDisclosureRepo struct {
	*createIdempotencyRepoStub
	scoped      []DuplicateCandidate
	scopedErr   error
	scopedCalls int
}

func (r *duplicateDisclosureRepo) FindDuplicatesScoped(context.Context, auth.Principal, string, string, uint64) ([]DuplicateCandidate, error) {
	r.scopedCalls++
	if r.scopedErr != nil {
		return nil, r.scopedErr
	}
	return append([]DuplicateCandidate(nil), r.scoped...), nil
}

func duplicateSalesPrincipal(userID string) auth.Principal {
	return auth.Principal{
		TenantID: "tenant-a", UserID: userID, PrimaryOrgID: "org-a", OrganizationIDs: []string{"org-a"},
		Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf,
		Permissions: map[string]struct{}{"customer.create": {}, "customer.update": {}},
	}
}

func duplicateConflictDetails(t *testing.T, err error) (DuplicateConflictDetails, string) {
	t.Helper()
	appErr := apperror.As(err)
	details, ok := appErr.Details.(DuplicateConflictDetails)
	if !ok {
		t.Fatalf("error details=%T, want DuplicateConflictDetails", appErr.Details)
	}
	payload, marshalErr := json.Marshal(details)
	if marshalErr != nil {
		t.Fatalf("marshal details: %v", marshalErr)
	}
	return details, string(payload)
}

func TestCustomerCreateHiddenDuplicateDoesNotDiscloseCandidateSummary(t *testing.T) {
	hidden := DuplicateCandidate{ID: 99123, CustomerNo: "KH-SECRET-99123", Name: "他人客户甲", Status: StatusActive, ExactCode: true}
	repo := &duplicateDisclosureRepo{createIdempotencyRepoStub: &createIdempotencyRepoStub{duplicates: []DuplicateCandidate{hidden}}}
	service := newCreateTestService(t, repo, &createAuditStub{})

	_, err := service.Create(auth.WithPrincipal(context.Background(), duplicateSalesPrincipal("sales-a")), createTestRequest("hidden-duplicate"))
	if !errors.Is(err, ErrDuplicateCode) {
		t.Fatalf("error=%v, want duplicate credit code", err)
	}
	details, payload := duplicateConflictDetails(t, err)
	if len(details.VisibleCandidates) != 0 || !details.RestrictedConflict || !details.AdminReviewRequired {
		t.Fatalf("details=%+v", details)
	}
	for _, forbidden := range []string{hidden.CustomerNo, hidden.Name, hidden.Status, "99123"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("hidden candidate field %q leaked in %s", forbidden, payload)
		}
	}
	if repo.scopedCalls != 1 {
		t.Fatalf("scoped duplicate lookups=%d, want 1", repo.scopedCalls)
	}
}

func TestCustomerCreateHiddenNameDuplicateReturnsSafeConflictOnly(t *testing.T) {
	hidden := DuplicateCandidate{ID: 99234, CustomerNo: "KH-SECRET-99234", Name: "他人客户乙", Status: StatusActive}
	repo := &duplicateDisclosureRepo{createIdempotencyRepoStub: &createIdempotencyRepoStub{duplicates: []DuplicateCandidate{hidden}}}
	service := newCreateTestService(t, repo, &createAuditStub{})

	_, err := service.Create(auth.WithPrincipal(context.Background(), duplicateSalesPrincipal("sales-a")), createTestRequest("hidden-name-duplicate"))
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("error=%v, want duplicate name", err)
	}
	details, payload := duplicateConflictDetails(t, err)
	if len(details.VisibleCandidates) != 0 || !details.RestrictedConflict || !details.AdminReviewRequired {
		t.Fatalf("details=%+v", details)
	}
	for _, forbidden := range []string{hidden.CustomerNo, hidden.Name, hidden.Status, "99234"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("hidden candidate field %q leaked in %s", forbidden, payload)
		}
	}
}

func TestCustomerCreateReturnsOnlyVisibleDuplicateCandidateSummary(t *testing.T) {
	visible := DuplicateCandidate{ID: 88001, CustomerNo: "KH-VISIBLE-88001", Name: "本人客户丙", Status: StatusActive}
	hidden := DuplicateCandidate{ID: 88002, CustomerNo: "KH-HIDDEN-88002", Name: "他人客户丁", Status: StatusActive}
	repo := &duplicateDisclosureRepo{
		createIdempotencyRepoStub: &createIdempotencyRepoStub{duplicates: []DuplicateCandidate{visible, hidden}},
		scoped:                    []DuplicateCandidate{visible},
	}
	service := newCreateTestService(t, repo, &createAuditStub{})

	_, err := service.Create(auth.WithPrincipal(context.Background(), duplicateSalesPrincipal("sales-a")), createTestRequest("mixed-duplicate"))
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("error=%v, want duplicate name", err)
	}
	details, payload := duplicateConflictDetails(t, err)
	if len(details.VisibleCandidates) != 1 || details.VisibleCandidates[0].ID != visible.ID || !details.RestrictedConflict || !details.AdminReviewRequired {
		t.Fatalf("details=%+v", details)
	}
	if !strings.Contains(payload, visible.CustomerNo) || !strings.Contains(payload, visible.Name) {
		t.Fatalf("visible candidate summary missing: %s", payload)
	}
	for _, forbidden := range []string{hidden.CustomerNo, hidden.Name, "88002"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("hidden candidate field %q leaked in %s", forbidden, payload)
		}
	}
}

type duplicateUpdateRepo struct {
	Repository
	current    *Customer
	duplicates []DuplicateCandidate
	scoped     []DuplicateCandidate
}

func (r *duplicateUpdateRepo) FindByID(context.Context, auth.Principal, uint64, bool) (*Customer, error) {
	value := *r.current
	return &value, nil
}

func (r *duplicateUpdateRepo) FindDuplicates(context.Context, string, string, string, uint64) ([]DuplicateCandidate, error) {
	return append([]DuplicateCandidate(nil), r.duplicates...), nil
}

func (r *duplicateUpdateRepo) FindDuplicatesScoped(context.Context, auth.Principal, string, string, uint64) ([]DuplicateCandidate, error) {
	return append([]DuplicateCandidate(nil), r.scoped...), nil
}

func TestCustomerUpdateHiddenDuplicateDoesNotDiscloseCandidateSummary(t *testing.T) {
	hidden := DuplicateCandidate{ID: 99345, CustomerNo: "KH-SECRET-99345", Name: "他人客户戊", Status: StatusActive}
	repo := &duplicateUpdateRepo{
		current: &Customer{
			Model: database.Model{ID: 7, TenantID: "tenant-a", Version: 2},
			Name:  "本人客户", NormalizedName: normalizeName("本人客户"), CustomerType: "企业",
			Industry: "软件", Region: "华东", OwnerUserID: "sales-a", OwnerOrgID: "org-a", Status: StatusActive,
		},
		duplicates: []DuplicateCandidate{hidden},
	}
	service := NewService(nil, repo, &createAuditStub{}, profileCodec(t))
	request := UpdateRequest{
		Name: "本人客户", CustomerType: "企业", Industry: "软件", Region: "华东",
		OwnerUserID: "sales-a", OwnerOrgID: "org-a",
		Contacts: []UpdateContactInput{{Name: "张三", IsRegistration: true}},
		Version:  2, Reason: "更新客户资料",
	}

	_, err := service.Update(auth.WithPrincipal(context.Background(), duplicateSalesPrincipal("sales-a")), 7, request)
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("error=%v, want duplicate name", err)
	}
	details, payload := duplicateConflictDetails(t, err)
	if len(details.VisibleCandidates) != 0 || !details.RestrictedConflict || !details.AdminReviewRequired {
		t.Fatalf("details=%+v", details)
	}
	for _, forbidden := range []string{hidden.CustomerNo, hidden.Name, hidden.Status, "99345"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("hidden candidate field %q leaked in %s", forbidden, payload)
		}
	}
}
