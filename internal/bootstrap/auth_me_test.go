package bootstrap

import (
	"os"
	"strings"
	"testing"
)

func TestAuthMeResponseIncludesOrganizationClaims(t *testing.T) {
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, expected := range []string{
		`"primary_org_id": principal.PrimaryOrgID`,
		`"organization_ids": append([]string{}, principal.OrganizationIDs...)`,
		// 统一会话空闲窗口字段与活动上报路由必须真实接线，不能只存在于服务层。
		`data["idle_timeout_seconds"] = int(principal.SessionIdleTimeout / time.Second)`,
		`data["idle_remaining_ms"] = remaining`,
		`api.POST("/auth/activity", authHandler.Activity)`,
		`SessionIdleTTL: config.OIDCSessionIdleTTL`,
		`SessionAbsoluteTTL: config.OIDCSessionAbsoluteTTL`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("auth/me response missing %s", expected)
		}
	}
}
