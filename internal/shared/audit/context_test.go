package audit

import (
	"context"
	"testing"
)

// AUD-IP-2026：业务事件由服务层写入，拿不到请求；入口把来源放进上下文后，
// 审计写入必须自动补齐登录地址、操作地址与来源标记。
func TestMetaFromReturnsEntryProvenance(t *testing.T) {
	ctx := WithMeta(context.Background(), Meta{LoginIP: "203.0.113.10", ClientIP: "10.8.0.5", IPSource: "TRUSTED_PROXY"})
	meta := MetaFrom(ctx)
	if meta.LoginIP != "203.0.113.10" || meta.ClientIP != "10.8.0.5" || meta.IPSource != "TRUSTED_PROXY" {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestMetaFromEmptyContextIsZero(t *testing.T) {
	if meta := MetaFrom(context.Background()); meta != (Meta{}) {
		t.Fatalf("meta = %#v, want zero", meta)
	}
}

// 调用方显式提供的值优先于上下文，避免覆盖更精确的来源。
func TestExplicitEventProvenanceWinsOverContext(t *testing.T) {
	ctx := WithMeta(context.Background(), Meta{ClientIP: "10.8.0.5", IPSource: "TRUSTED_PROXY"})
	event := Event{ClientIP: "198.51.100.7", IPSource: "DIRECT"}
	meta := MetaFrom(ctx)
	clientIP, ipSource := event.ClientIP, event.IPSource
	if clientIP == "" {
		clientIP = meta.ClientIP
	}
	if ipSource == "" {
		ipSource = meta.IPSource
	}
	if clientIP != "198.51.100.7" || ipSource != "DIRECT" {
		t.Fatalf("clientIP=%q ipSource=%q，显式值必须优先", clientIP, ipSource)
	}
}
