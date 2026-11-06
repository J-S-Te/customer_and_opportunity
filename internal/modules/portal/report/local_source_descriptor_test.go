package report

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOptionalSourcePreservesFrozenLegacyDescriptor 验证旧描述符密文原文及摘要完全不变。
// 参数：t为测试上下文。
// 返回值：无；缺省新增字段改变历史序列化或本地来源丢失时失败。
func TestOptionalSourcePreservesFrozenLegacyDescriptor(t *testing.T) {
	value := FileDescriptor{ObjectRef: "file-1", FileName: "report.pdf", MIME: "application/pdf", FileHash: strings.Repeat("a", 64), Size: 7}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"ObjectRef":"file-1","FileName":"report.pdf","MIME":"application/pdf","FileHash":"` + strings.Repeat("a", 64) + `","Size":7}`
	if string(raw) != expected {
		t.Fatalf("legacy descriptor changed: %s", raw)
	}
	old := descriptorHash(value)
	value.SourceType = "PORTAL_LOCAL"
	raw, err = json.Marshal(value)
	if err != nil || !strings.Contains(string(raw), `"SourceType":"PORTAL_LOCAL"`) || descriptorHash(value) == old {
		t.Fatal("local source not frozen")
	}
}
