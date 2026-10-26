package middleware

import (
	"os"
	"strings"
	"testing"
)

// 统一审计方案 §2.3：登录地址缺失时保持未知，不能用当前请求（可伪造的转发头）回填。
func TestRequestAuditDoesNotBackfillLoginIPFromRequest(t *testing.T) {
	source, err := os.ReadFile("request_audit.go")
	if err != nil {
		t.Fatalf("读取 request_audit.go: %v", err)
	}
	if strings.Contains(string(source), "loginip.FromRequest") {
		t.Fatal("请求审计不得用当前请求地址回填登录地址")
	}
}

// 登录回调同样不得用请求头覆盖权威登录地址。
func TestLoginCompletionDoesNotTrustRequestHeadersForLoginIP(t *testing.T) {
	source, err := os.ReadFile("../modules/crmauth/handler.go")
	if err != nil {
		t.Fatalf("读取 crmauth/handler.go: %v", err)
	}
	if strings.Contains(string(source), "loginip.FromRequest") {
		t.Fatal("登录回调不得把转发头当作登录地址")
	}
}
