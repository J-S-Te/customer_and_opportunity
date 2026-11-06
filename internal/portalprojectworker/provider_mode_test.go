package portalprojectworker

import (
	"context"
	"testing"
)

// TestLocalDirectoryRejectsLegacyProjectWorkerBeforeDependencies 验证本地模式不创建旧项目HTTP或DB依赖。
// 参数：t为测试上下文。
// 返回值：无；local或未知模式启动成功时失败。
func TestLocalDirectoryRejectsLegacyProjectWorkerBeforeDependencies(t *testing.T) {
	for _, mode := range []string{"local", "LOCAL", "unknown"} {
		if _, err := New(context.Background(), Config{LocalDirectoryMode: mode}); err == nil {
			t.Fatalf("mode %q created legacy project worker", mode)
		}
	}
	t.Setenv("PORTAL_CUSTOMER_DIRECTORY_MODE", "local")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("environment local mode accepted")
	}
	if err := validateDirectoryMode(""); err != nil {
		t.Fatal("default CRM rejected", err)
	}
}
