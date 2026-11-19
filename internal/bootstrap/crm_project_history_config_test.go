package bootstrap

import (
	"strings"
	"testing"
)

// TestLoadConfigCRMProjectHistory 核对直连接入的显式开启、专用 scope 和凭据要求。
// 参数：t 为测试上下文。
// 返回值：无。
func TestLoadConfigCRMProjectHistory(t *testing.T) {
	setBaseConfig(t)
	config, err := LoadConfig()
	if err != nil || config.CRMProjectHistoryEnabled {
		t.Fatalf("direct history must be optional: %v", err)
	}
	t.Setenv("CRM_PROJECT_HISTORY_ENABLED", "true")
	t.Setenv("CRM_PROJECT_HISTORY_URL", "http://project-api:8082/internal/v1/crm/customers")
	t.Setenv("CRM_PROJECT_HISTORY_TOKEN_URL", "http://platform-api:8080/oauth2/token")
	t.Setenv("CRM_PROJECT_HISTORY_CLIENT_ID", "customer_and_opportunity-prod-crm-project-history")
	t.Setenv("CRM_PROJECT_HISTORY_CLIENT_SECRET", "PENDING_ONBOARDING")
	if _, err = LoadConfig(); err == nil || !strings.Contains(err.Error(), "CRM_PROJECT_HISTORY_CLIENT_SECRET") {
		t.Fatalf("placeholder credential was accepted: %v", err)
	}
	t.Setenv("CRM_PROJECT_HISTORY_CLIENT_SECRET", "fixture-secret")
	t.Setenv("CRM_PROJECT_HISTORY_SCOPE", "dashboard.project.read")
	if _, err = LoadConfig(); err == nil || !strings.Contains(err.Error(), "CRM_PROJECT_HISTORY_SCOPE") {
		t.Fatalf("unexpected scope was accepted: %v", err)
	}
	t.Setenv("CRM_PROJECT_HISTORY_SCOPE", "crm.project_history.read")
	config, err = LoadConfig()
	if err != nil || !config.CRMProjectHistoryEnabled || !runtimeCapabilities(config)["customer_project_history"].Available {
		t.Fatalf("valid history configuration was not enabled: %v", err)
	}
}
