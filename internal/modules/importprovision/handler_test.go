package importprovision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// fakeCustomerProvisioner 记录通道调用参数，供端点契约断言使用。
type fakeCustomerProvisioner struct {
	lookup       customer.ProvisionLookup
	lookupErr    error
	createResult *customer.Response
	createErr    error
	lookupNames  []string
	createInputs []customer.ProvisionCreateRequest
}

func (f *fakeCustomerProvisioner) LookupProvisionCandidates(_ context.Context, name string) (customer.ProvisionLookup, error) {
	f.lookupNames = append(f.lookupNames, name)
	return f.lookup, f.lookupErr
}

func (f *fakeCustomerProvisioner) CreateProvision(_ context.Context, request customer.ProvisionCreateRequest) (*customer.Response, error) {
	f.createInputs = append(f.createInputs, request)
	return f.createResult, f.createErr
}

// fakeOpportunityProvisioner 记录商机通道调用参数。
type fakeOpportunityProvisioner struct {
	createResult *opportunity.Response
	createErr    error
	createInputs []opportunity.ProvisionCreateRequest
	signResult   *opportunity.Response
	signReplay   bool
	signErr      error
	signInputs   map[uint64]opportunity.ProvisionSignRequest
}

func (f *fakeOpportunityProvisioner) CreateProvision(_ context.Context, request opportunity.ProvisionCreateRequest) (*opportunity.Response, error) {
	f.createInputs = append(f.createInputs, request)
	return f.createResult, f.createErr
}

func (f *fakeOpportunityProvisioner) SignProvision(_ context.Context, id uint64, request opportunity.ProvisionSignRequest) (*opportunity.Response, bool, error) {
	if f.signInputs == nil {
		f.signInputs = map[uint64]opportunity.ProvisionSignRequest{}
	}
	f.signInputs[id] = request
	return f.signResult, f.signReplay, f.signErr
}

// provisionRouter 按生产装配方式注册路由：机器认证主体由外层中间件注入，
// 端点自身的 scope 校验仍由 middleware.RequirePermission 执行。
func provisionRouter(principal auth.Principal, service *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/internal", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
	})
	RegisterInternalRoutes(group, NewHandler(service))
	return router
}

func machinePrincipal(scopes ...string) auth.Principal {
	permissions := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		permissions[scope] = struct{}{}
	}
	return auth.Principal{TenantID: "tenant-a", UserID: "machine:contract-system", Roles: []string{"machine"}, Permissions: permissions, ScopeMode: auth.ScopeAll}
}

func perform(router *gin.Engine, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func envelope(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %s", recorder.Body.String())
	}
	return body
}

func TestProvisionRoutesRequireDedicatedScope(t *testing.T) {
	// 缺少通道 scope 时四个端点都必须 403，且不得触达领域服务。
	missing := provisionRouter(machinePrincipal("customer.create", "opportunity.create"),
		NewService(&fakeCustomerProvisioner{}, &fakeOpportunityProvisioner{}))
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/internal/import-provision/customers/lookup", `{"name":"历史客户甲"}`},
		{http.MethodPost, "/api/v1/internal/import-provision/customers", `{"name":"历史客户甲","owner_identity_id":"owner-x","contacts":[{"name":"张三","phone":"13800138000","is_registration":true}]}`},
		{http.MethodPost, "/api/v1/internal/import-provision/opportunities", `{"name":"商机","customer_id":3,"owner_identity_id":"owner-x","expected_amount":"100.00","expected_sign_date":"2020-03-01","requirement_summary":"需求"}`},
		{http.MethodPost, "/api/v1/internal/import-provision/opportunities/7/sign", `{"contract_ref":"HT-1","expected_version":7,"reason":"推进"}`},
	}
	for _, item := range cases {
		recorder := perform(missing, item.method, item.path, item.body, map[string]string{"X-Actor-Identity-ID": "actor-9", "Idempotency-Key": "key"})
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s 缺 scope 状态=%d body=%s", item.path, recorder.Code, recorder.Body.String())
		}
		if code := envelope(t, recorder)["code"]; code != "COMMON_FORBIDDEN" {
			t.Fatalf("%s 缺 scope code=%v", item.path, code)
		}
	}

	// 反向：只持 customer scope 的机器身份不能进入商机通道，反之亦然。
	customerOnly := provisionRouter(machinePrincipal(customer.ProvisionScope),
		NewService(&fakeCustomerProvisioner{}, &fakeOpportunityProvisioner{}))
	recorder := perform(customerOnly, http.MethodPost, "/api/v1/internal/import-provision/opportunities",
		`{"name":"商机","customer_id":3,"owner_identity_id":"owner-x","expected_amount":"100.00","expected_sign_date":"2020-03-01","requirement_summary":"需求"}`,
		map[string]string{"X-Actor-Identity-ID": "actor-9", "Idempotency-Key": "key"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("跨通道 scope 状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
	opportunityOnly := provisionRouter(machinePrincipal(opportunity.ProvisionScope),
		NewService(&fakeCustomerProvisioner{}, &fakeOpportunityProvisioner{}))
	recorder = perform(opportunityOnly, http.MethodPost, "/api/v1/internal/import-provision/customers/lookup", `{"name":"历史客户甲"}`,
		map[string]string{"X-Actor-Identity-ID": "actor-9"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("跨通道 scope 状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProvisionLookupEndpointReturnsCandidatesAndOrigin(t *testing.T) {
	customers := &fakeCustomerProvisioner{lookup: customer.ProvisionLookup{
		Candidates:     []customer.ProvisionCandidate{{ID: 12, Name: "历史客户甲", Status: customer.StatusActive, Enabled: true}},
		Unique:         true,
		NormalizedName: "历史客户甲",
	}}
	router := provisionRouter(machinePrincipal(customer.ProvisionScope), NewService(customers, &fakeOpportunityProvisioner{}))
	recorder := perform(router, http.MethodPost, "/api/v1/internal/import-provision/customers/lookup",
		`{"name":"历史客户甲"}`, map[string]string{"X-Actor-Identity-ID": "actor-9"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("lookup 状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
	data := envelope(t, recorder)["data"].(map[string]any)
	if data["origin"] != Origin || data["unique"] != true || data["normalized_name"] != "历史客户甲" {
		t.Fatalf("lookup data=%#v", data)
	}
	if len(customers.lookupNames) != 1 || customers.lookupNames[0] != "历史客户甲" {
		t.Fatalf("lookup 入参=%#v", customers.lookupNames)
	}
}

func TestProvisionEndpointsRequireActorIdentityHeader(t *testing.T) {
	router := provisionRouter(machinePrincipal(customer.ProvisionScope, opportunity.ProvisionScope),
		NewService(&fakeCustomerProvisioner{}, &fakeOpportunityProvisioner{}))
	recorder := perform(router, http.MethodPost, "/api/v1/internal/import-provision/customers/lookup", `{"name":"历史客户甲"}`, nil)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺 actor 头状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if code := envelope(t, recorder)["code"]; code != "CRM_IMPORT_PROVISION_ACTOR_INVALID" {
		t.Fatalf("缺 actor 头 code=%v", code)
	}
}

func TestProvisionCustomerEndpointRequiresIdempotencyKeyAndPassesActor(t *testing.T) {
	customers := &fakeCustomerProvisioner{createResult: &customer.Response{ID: 12, CustomerNo: "KH1", Name: "历史客户甲", Status: customer.StatusActive,
		CustomerType: customer.DefaultProvisionCustomerType, Industry: customer.DefaultProvisionIndustry, Region: customer.DefaultProvisionRegion,
		OwnerUserID: "owner-x", OwnerOrgID: "org-a", Version: 1}}
	router := provisionRouter(machinePrincipal(customer.ProvisionScope), NewService(customers, &fakeOpportunityProvisioner{}))
	body := `{"name":"历史客户甲","owner_identity_id":"owner-x","contacts":[{"name":"张三","phone":"13800138000","is_registration":true}]}`

	recorder := perform(router, http.MethodPost, "/api/v1/internal/import-provision/customers", body, map[string]string{"X-Actor-Identity-ID": "actor-9"})
	if recorder.Code != http.StatusUnprocessableEntity || envelope(t, recorder)["code"] != "COMMON_IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("缺幂等键状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(customers.createInputs) != 0 {
		t.Fatal("缺幂等键仍调用了领域服务")
	}

	recorder = perform(router, http.MethodPost, "/api/v1/internal/import-provision/customers", body,
		map[string]string{"X-Actor-Identity-ID": "actor-9", "Idempotency-Key": "provision-key"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("创建客户状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(customers.createInputs) != 1 {
		t.Fatalf("创建调用=%#v", customers.createInputs)
	}
	input := customers.createInputs[0]
	// X-Actor-Identity-ID 只做审计归属；负责人来自 owner_identity_id。
	if input.ActorIdentityID != "actor-9" || input.OwnerIdentityID != "owner-x" || input.IdempotencyKey != "provision-key" {
		t.Fatalf("通道入参=%#v", input)
	}
	data := envelope(t, recorder)["data"].(map[string]any)
	if data["origin"] != Origin || data["owner_identity_id"] != "owner-x" || data["customer_no"] != "KH1" {
		t.Fatalf("创建响应 data=%#v", data)
	}
}

func TestProvisionOpportunityEndpointsDelegateAndReportReplay(t *testing.T) {
	opportunities := &fakeOpportunityProvisioner{
		createResult: &opportunity.Response{ID: 21, OpportunityNo: "SJ1", Name: "历史商机甲", CustomerID: 3, Type: opportunity.DefaultProvisionOpportunityType,
			Source: opportunity.DefaultProvisionOpportunitySource, ExpectedAmount: "100.00", ExpectedSignDate: "2020-03-01",
			OwnerUserID: "owner-x", OwnerOrgID: "org-a", CurrentStage: opportunity.StageInitial, Status: opportunity.StatusFollowing, Version: 1},
		signResult: &opportunity.Response{ID: 21, OpportunityNo: "SJ1", Name: "历史商机甲", CustomerID: 3,
			CurrentStage: opportunity.StageSigned, Status: opportunity.StatusClosed, Version: 2},
		signReplay: true,
	}
	router := provisionRouter(machinePrincipal(opportunity.ProvisionScope), NewService(&fakeCustomerProvisioner{}, opportunities))
	createBody := `{"name":"历史商机甲","customer_id":3,"owner_identity_id":"owner-x","expected_amount":"100.00","expected_sign_date":"2020-03-01","requirement_summary":"需求"}`
	recorder := perform(router, http.MethodPost, "/api/v1/internal/import-provision/opportunities", createBody,
		map[string]string{"X-Actor-Identity-ID": "actor-9", "Idempotency-Key": "opportunity-key"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("创建商机状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
	created := opportunities.createInputs[0]
	if created.ActorIdentityID != "actor-9" || created.OwnerIdentityID != "owner-x" || created.ExpectedSignDate != "2020-03-01" {
		t.Fatalf("商机通道入参=%#v", created)
	}
	data := envelope(t, recorder)["data"].(map[string]any)
	if data["origin"] != Origin || data["current_stage"] != opportunity.StageInitial || data["opp_status"] != opportunity.StatusFollowing {
		t.Fatalf("创建商机响应 data=%#v", data)
	}

	signBody := `{"contract_ref":"HT-1","contract_id":"01JCONTRACT","expected_version":1,"reason":"历史合同已入库","external_record_id":"row-1"}`
	recorder = perform(router, http.MethodPost, "/api/v1/internal/import-provision/opportunities/21/sign", signBody,
		map[string]string{"X-Actor-Identity-ID": "actor-9"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("推进状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
	signInput, ok := opportunities.signInputs[21]
	if !ok || signInput.ContractRef != "HT-1" || signInput.ContractID != "01JCONTRACT" || signInput.ExpectedVersion != 1 || signInput.ActorIdentityID != "actor-9" {
		t.Fatalf("推进入参=%#v", opportunities.signInputs)
	}
	signData := envelope(t, recorder)["data"].(map[string]any)
	if signData["origin"] != Origin || signData["current_stage"] != opportunity.StageSigned || signData["opp_status"] != opportunity.StatusClosed || signData["idempotent_replay"] != true {
		t.Fatalf("推进响应 data=%#v", signData)
	}

	recorder = perform(router, http.MethodPost, "/api/v1/internal/import-provision/opportunities/0/sign", signBody,
		map[string]string{"X-Actor-Identity-ID": "actor-9"})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法商机标识状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProvisionEndpointsFailClosedWhenUnwired(t *testing.T) {
	router := provisionRouter(machinePrincipal(customer.ProvisionScope, opportunity.ProvisionScope), NewService(nil, nil))
	recorder := perform(router, http.MethodPost, "/api/v1/internal/import-provision/customers/lookup", `{"name":"历史客户甲"}`,
		map[string]string{"X-Actor-Identity-ID": "actor-9"})
	if recorder.Code != http.StatusServiceUnavailable || envelope(t, recorder)["code"] != "CRM_IMPORT_PROVISION_UNAVAILABLE" {
		t.Fatalf("未装配通道状态=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
