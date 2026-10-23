package contracttransferworker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestContractIntakeAgainstRealReceiver 使用生产 HTTP 客户端访问
// 已部署的合同接收端点。须在平台 Docker
// 网络内运行，以验证服务 DNS 和真实机器令牌交换。
func TestContractIntakeAgainstRealReceiver(t *testing.T) {
	intakeURL := strings.TrimSpace(os.Getenv("CONTRACT_OPPORTUNITY_INTAKE_URL"))
	tokenURL := strings.TrimSpace(os.Getenv("CONTRACT_TOKEN_URL"))
	clientID := strings.TrimSpace(os.Getenv("CONTRACT_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("CONTRACT_CLIENT_SECRET"))
	tenantID := strings.TrimSpace(os.Getenv("OIDC_TENANT_ID"))
	if intakeURL == "" || tokenURL == "" || clientID == "" || clientSecret == "" || tenantID == "" {
		t.Skip("real contract receiver E2E environment is not configured")
	}
	client, err := newContractClient(Config{
		TokenURL: tokenURL, ClientID: clientID, ClientSecret: clientSecret,
		Scope: "opportunity.signed.write", IntakeURL: intakeURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventID := fmt.Sprintf("qa-contract-e2e-%d", time.Now().UTC().UnixNano())
	result, err := client.deliver(context.Background(), signedCommand{
		EventID: eventID, TenantID: tenantID, OpportunityID: uint64(time.Now().UTC().UnixNano() % 1_000_000_000),
		EventVersion: 1, OpportunityNo: "QA-CONTRACT-E2E", CustomerID: 1, ContractRef: "HT-QA-E2E",
		ExpectedAmount: "100.00", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("real contract receiver rejected intake: %v", err)
	}
	if result.IntakeID == "" || result.Status != "ACCEPTED" {
		t.Fatalf("real contract receiver result=%+v", result)
	}
}
