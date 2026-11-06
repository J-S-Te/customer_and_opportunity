package portalreportworker

import "testing"

// TestLocalDirectoryRejectsLegacyReportDeliveryBeforeDependencies 验证LOCAL不投递伪PMS申请。
// 参数：t为测试上下文。
// 返回值：无；local全量投递或未知来源启动成功时失败。
func TestLocalDirectoryRejectsLegacyReportDeliveryBeforeDependencies(t *testing.T) {
	if _, err := New(Config{LocalDirectoryMode: "local", IngestOnly: false}); err == nil {
		t.Fatal("local mode created external delivery worker")
	}
	if _, err := New(Config{LocalDirectoryMode: "unknown", IngestOnly: true}); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
