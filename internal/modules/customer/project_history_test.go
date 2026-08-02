package customer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

func TestHTTPProjectHistoryReaderUsesDedicatedClientCredentialsAndFreshReplayHeaders(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	nonces := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "crm-project" || secret != "machine-secret" {
				t.Errorf("unexpected token credentials: %q %q %v", clientID, secret, ok)
			}
			_ = request.ParseForm()
			if request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != projectHistoryScope || request.Form.Get("audience") != "" {
				t.Errorf("unexpected token form: %v", request.Form)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"access_token":"signed","token_type":"Bearer","expires_in":3600,"scope":"portal.project_history.read"}`)
		case "/customer-portal/internal/customers/7/projects":
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer signed" || request.URL.Query().Get("page") != "2" || request.URL.Query().Get("page_size") != "10" {
				t.Errorf("unexpected resource request: %s %s %#v", request.Method, request.URL.String(), request.Header)
			}
			if request.Header.Get("X-Integration-Timestamp") != now.Format(time.RFC3339Nano) || request.Header.Get("X-Request-ID") != "request-1" || request.Header.Get("X-Tenant-ID") != "" {
				t.Errorf("unexpected integration headers: %#v", request.Header)
			}
			nonce := request.Header.Get("X-Integration-Nonce")
			decoded, err := base64.RawURLEncoding.DecodeString(nonce)
			if err != nil || len(decoded) != 32 {
				t.Errorf("invalid nonce %q: %v", nonce, err)
			}
			nonces = append(nonces, nonce)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"code":"OK","message":"success","request_id":"portal-request","data":{"items":[{"project_id":"P-1","project_name":"统一身份项目","contract_no":"HT-1","status":"EXECUTING","progress_pct":60,"current_stage":"实施","source_updated_at":"2026-08-01T07:58:00Z","synced_at":"2026-08-01T07:59:00Z","sync_last_success_at":"2026-08-01T07:59:00Z","stale":false,"staleness_seconds":60}],"page":2,"page_size":10,"total":11}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	reader, err := NewHTTPProjectHistoryReader(context.Background(), ProjectHistoryReaderOptions{
		Endpoint: server.URL + "/customer-portal/internal/customers", TokenURL: server.URL + "/oauth2/token",
		ClientID: "crm-project", ClientSecret: "machine-secret", Scope: projectHistoryScope,
		HTTPClient: server.Client(), Now: func() time.Time { return now }, NonceReader: strings.NewReader(strings.Repeat("a", 32) + strings.Repeat("b", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestctx.WithID(context.Background(), "request-1")
	for range 2 {
		page, readErr := reader.ListCustomerProjects(ctx, "tenant-a", 7, 2, 10)
		if readErr != nil || len(page.Items) != 1 || page.Items[0].ProjectID != "P-1" {
			t.Fatalf("page=%#v err=%v", page, readErr)
		}
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Fatalf("each resource request must use a new nonce: %v", nonces)
	}
}

func TestHTTPProjectHistoryReaderRejectsInvalidEnvelopeAndBounds(t *testing.T) {
	validData := `{"items":[],"page":1,"page_size":20,"total":0}`
	tests := []struct {
		name string
		body string
	}{
		{name: "missing request id", body: `{"code":"OK","message":"success","data":` + validData + `}`},
		{name: "request id with newline", body: `{"code":"OK","message":"success","request_id":"bad\nrequest","data":` + validData + `}`},
		{name: "unknown field", body: `{"code":"OK","message":"success","request_id":"r","secret":"leak","data":` + validData + `}`},
		{name: "wrong page", body: `{"code":"OK","message":"success","request_id":"r","data":{"items":[],"page":2,"page_size":20,"total":0}}`},
		{name: "contradictory page total", body: `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"project_id":"p","project_name":"n","status":"s","progress_pct":1,"current_stage":"c","source_updated_at":"2026-08-01T00:00:00Z","synced_at":"2026-08-01T00:00:00Z","sync_last_success_at":"2026-08-01T00:00:00Z","stale":false,"staleness_seconds":0}],"page":2,"page_size":20,"total":1}}`},
		{name: "invalid project", body: `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"project_id":"","project_name":"n","status":"s","progress_pct":1,"current_stage":"c","source_updated_at":"2026-08-01T00:00:00Z","synced_at":"2026-08-01T00:00:00Z","sync_last_success_at":"2026-08-01T00:00:00Z","stale":false,"staleness_seconds":0}],"page":1,"page_size":20,"total":1}}`},
		{name: "missing successful sync freshness", body: `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"project_id":"p","project_name":"n","status":"s","progress_pct":1,"current_stage":"c","source_updated_at":"2026-08-01T00:00:00Z","synced_at":"2026-08-01T00:00:00Z","stale":false,"staleness_seconds":0}],"page":1,"page_size":20,"total":1}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := testProjectHistoryReader(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return jsonHTTPResponse(http.StatusOK, test.body), nil
			}))
			if _, err := reader.ListCustomerProjects(context.Background(), "tenant-a", 7, 1, 20); err == nil || !strings.Contains(err.Error(), "invalid response") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestHTTPProjectHistoryReaderRejectsPageBeyondPortalContract(t *testing.T) {
	reader := testProjectHistoryReader(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("dependency must not be called for an invalid page")
		return nil, nil
	}))
	if _, err := reader.ListCustomerProjects(context.Background(), "tenant-a", 7, maxProjectHistoryPage+1, 20); err == nil {
		t.Fatal("page beyond the Portal contract was accepted")
	}
}

func TestHTTPProjectHistoryReaderAcceptsExplicitNeverSuccessfullySyncedState(t *testing.T) {
	reader := testProjectHistoryReader(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"project_id":"p","project_name":"n","status":"s","progress_pct":1,"current_stage":"c","source_updated_at":"2026-08-01T00:00:00Z","synced_at":"2026-08-01T00:00:00Z","sync_last_success_at":null,"stale":true,"staleness_seconds":null}],"page":1,"page_size":20,"total":1}}`), nil
	}))
	page, err := reader.ListCustomerProjects(context.Background(), "tenant-a", 7, 1, 20)
	if err != nil || len(page.Items) != 1 || !page.Items[0].Stale || page.Items[0].SyncLastSuccessAt != nil || page.Items[0].StalenessSeconds != nil {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestHTTPProjectHistoryReaderAcceptsSnapshotWrittenAfterLastCompleteSync(t *testing.T) {
	reader := testProjectHistoryReader(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"project_id":"p","project_name":"n","status":"s","progress_pct":1,"current_stage":"c","source_updated_at":"2026-08-01T00:02:00Z","synced_at":"2026-08-01T00:03:00Z","sync_last_success_at":"2026-08-01T00:01:00Z","stale":true,"staleness_seconds":600}],"page":1,"page_size":20,"total":1}}`), nil
	}))
	page, err := reader.ListCustomerProjects(context.Background(), "tenant-a", 7, 1, 20)
	if err != nil || len(page.Items) != 1 || !page.Items[0].Stale {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestProjectHistoryValidatesCustomerVisibilityBeforeDependency(t *testing.T) {
	repo := &projectHistoryCustomerRepository{customer: &Customer{}}
	repo.customer.ID, repo.customer.TenantID, repo.customer.OwnerUserID = 7, "tenant-a", "owner-a"
	reader := &projectHistoryReaderStub{page: ProjectHistoryPage{Items: []ProjectHistoryItem{}, Page: 1, PageSize: 20}}
	service := NewService(nil, repo, nil, nil).UseProjectHistoryReader(reader)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "owner-a", ScopeMode: auth.ScopeSelf})
	page, err := service.ProjectHistory(ctx, 7, 1, 20)
	if err != nil || page.Items == nil || reader.calls != 1 || repo.findCalls != 1 {
		t.Fatalf("page=%#v reader=%d find=%d err=%v", page, reader.calls, repo.findCalls, err)
	}
	repo.findErr = ErrNotFound
	if _, err = service.ProjectHistory(ctx, 8, 1, 20); !errors.Is(err, ErrNotFound) || reader.calls != 1 {
		t.Fatalf("visibility failure called dependency: calls=%d err=%v", reader.calls, err)
	}
}

type projectHistoryCustomerRepository struct {
	Repository
	customer  *Customer
	findErr   error
	findCalls int
}

func (r *projectHistoryCustomerRepository) FindByID(context.Context, auth.Principal, uint64, bool) (*Customer, error) {
	r.findCalls++
	return r.customer, r.findErr
}

type projectHistoryReaderStub struct {
	page  ProjectHistoryPage
	err   error
	calls int
}

func (r *projectHistoryReaderStub) ListCustomerProjects(context.Context, string, uint64, int, int) (ProjectHistoryPage, error) {
	r.calls++
	return r.page, r.err
}

func testProjectHistoryReader(t *testing.T, transport http.RoundTripper) *HTTPProjectHistoryReader {
	t.Helper()
	client := &http.Client{Transport: transport}
	reader, err := NewHTTPProjectHistoryReader(context.Background(), ProjectHistoryReaderOptions{
		Endpoint: "https://portal.example/customer-portal/internal/customers", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "crm", ClientSecret: "secret", Scope: projectHistoryScope, HTTPClient: client,
		NonceReader: strings.NewReader(strings.Repeat("n", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader.client = client
	return reader
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestProjectHistoryEndpointURLDoesNotAcceptCredentialsOrQuery(t *testing.T) {
	for _, endpoint := range []string{"https://user:secret@portal.example/internal/customers", "https://portal.example/internal/customers?tenant=other"} {
		_, err := NewHTTPProjectHistoryReader(context.Background(), ProjectHistoryReaderOptions{Endpoint: endpoint, TokenURL: "https://identity.example/token", ClientID: "id", ClientSecret: "secret", Scope: projectHistoryScope})
		if err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
}

func TestProjectHistoryTokenRequestEncodingHasNoAudience(t *testing.T) {
	values := url.Values{"grant_type": {"client_credentials"}, "scope": {projectHistoryScope}}
	if encoded := values.Encode(); strings.Contains(encoded, "audience") {
		t.Fatalf("unexpected audience: %s", encoded)
	}
}

func TestProjectHistoryResponseJSONCannotCarryManagerIdentity(t *testing.T) {
	raw, err := json.Marshal(ProjectHistoryItem{ProjectID: "P-1", ProjectName: "项目", Status: "EXECUTING", CurrentStage: "实施"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"manager_portal_account_id", "manager_contact", "person_ref", "tenant_id", "customer_id", "raw_version"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("sensitive field %q leaked in %s", forbidden, raw)
		}
	}
}
