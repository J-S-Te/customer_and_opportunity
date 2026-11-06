package reportsecurity

import (
	"context"
	"testing"
)

// TestLocalGatewayIngestUsesExplicitSourceAndRejectsUnknown 验证本地文件仍经过原扫描与加密vault。
// 参数：t为测试上下文。
// 返回值：无；未知来源触发读取、LOCAL来源丢失或安全证据缺失时失败。
func TestLocalGatewayIngestUsesExplicitSourceAndRejectsUnknown(t *testing.T) {
	raw := pdfFixture(t)
	d := descriptor(raw)
	d.SourceType = "PORTAL_LOCAL"
	store, job := storeFixture(t)
	source := &gatewayFixture{data: raw, version: "immutable-local-version"}
	ingestor, err := NewGatewayIngestor(source, store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ingestor.IngestForJob(context.Background(), job, d)
	if err != nil || source.input.SourceType != "PORTAL_LOCAL" || source.input.RequestID != job.RequestID || receipt.ScanStatus != "CLEAN" || receipt.EncryptionAlgorithm != "AES-256-GCM" {
		t.Fatalf("local security path bypassed: %+v %v", receipt, err)
	}
	d.SourceType = "UNKNOWN"
	opens := source.opens
	if _, err := ingestor.IngestForJob(context.Background(), job, d); err == nil || source.opens != opens {
		t.Fatal("unknown source reached gateway")
	}
	d.SourceType = "PORTAL_LOCAL"
	d.Size = (20 << 20) + 1
	if _, err := ingestor.IngestForJob(context.Background(), job, d); err == nil || source.opens != opens {
		t.Fatal("oversized local source reached gateway")
	}
}
