package importprovision

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
)

type crossRepositoryWire struct {
	Path     string            `json:"path"`
	Body     json.RawMessage   `json:"body"`
	Headers  map[string]string `json:"headers"`
	Status   int               `json:"status"`
	Response json.RawMessage   `json:"response"`
}

// TestContractImportProvisionCrossRepository 用真实路由验证合同客户端导出的HTTP字节。
// 参数：t为测试上下文，环境变量指定合同客户端请求及响应交换文件。
// 返回值：绑定校验、scope门禁、领域映射或成功信封不符时失败。
func TestContractImportProvisionCrossRepository(t *testing.T) {
	path := os.Getenv("CRM_PROVISION_CONTRACT_FILE")
	if path == "" {
		t.Skip("需要跨仓契约驱动脚本")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []crossRepositoryWire
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 {
		t.Fatalf("合同客户端请求数量=%d", len(cases))
	}
	customers := &fakeCustomerProvisioner{createResult: &customer.Response{ID: 7, Name: "历史客户甲", Status: customer.StatusActive, Version: 1}}
	opportunities := &fakeOpportunityProvisioner{createResult: &opportunity.Response{ID: 7, CustomerID: 7, Name: "历史商机", Version: 3}, signResult: &opportunity.Response{ID: 7, CustomerID: 7, Version: 4, Status: "CLOSED", CurrentStage: "已签约"}}
	router := provisionRouter(machinePrincipal(customer.ProvisionScope, opportunity.ProvisionScope), NewService(customers, opportunities))
	for i := range cases {
		row := &cases[i]
		recorder := perform(router, http.MethodPost, row.Path, string(row.Body), row.Headers)
		row.Status = recorder.Code
		row.Response = append(json.RawMessage(nil), recorder.Body.Bytes()...)
		if row.Status != 200 && row.Status != 201 {
			t.Errorf("合同请求被真实CRM路由拒绝: path=%s status=%d response=%s", row.Path, row.Status, row.Response)
		}
	}
	if len(customers.createInputs) != 1 || len(opportunities.createInputs) != 1 || len(opportunities.signInputs) != 1 {
		t.Fatalf("实际领域调用缺失: customers=%d opportunities=%d signs=%d", len(customers.createInputs), len(opportunities.createInputs), len(opportunities.signInputs))
	}
	if len(customers.createInputs) == 1 {
		input := customers.createInputs[0]
		if len(input.Contacts) != 1 || !input.Contacts[0].IsRegistration || input.Contacts[0].Name != "张三" {
			t.Errorf("联系人未完整映射: %+v", input.Contacts)
		}
	}
	if len(opportunities.createInputs) == 1 {
		input := opportunities.createInputs[0]
		if input.ExpectedAmount != "100.00" || input.RequirementSummary != "历史合同软件测试" {
			t.Errorf("商机字段未映射: %+v", input)
		}
	}
	if input, ok := opportunities.signInputs[7]; ok {
		if input.ExpectedVersion != 3 || input.Reason == "" {
			t.Errorf("签约版本/原因缺失: %+v", input)
		}
	}
	raw, err = json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
