package report

import (
	"context"
	"errors"
	"testing"
)

// TestExplicitSourceGuardCannotBeNilOrBypassed 验证显式来源ACL在grant与下载依赖之前运行。
// 参数：t为测试上下文。
// 返回值：无；缺校验器或来源拒绝仍触发原仓储/文件时失败。
func TestExplicitSourceGuardCannotBeNilOrBypassed(t *testing.T) {
	service := NewDownloadService(nil, nil, nil, nil, nil, nil, 0).UseSourceAccess(nil)
	actor := Actor{TenantID: "tenant", CustomerID: 1, AccountID: "real-subject"}
	if _, err := service.CreateGrant(context.Background(), actor, 7, GrantCommand{IdempotencyKey: "original"}); !errors.Is(err, ErrDownloadUnavailable) {
		t.Fatal("nil guard bypassed", err)
	}
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, "token", DownloadMetadata{}); !errors.Is(err, ErrDownloadUnavailable) {
		t.Fatal("nil download guard bypassed", err)
	}
	denied := errors.New("local access revoked")
	service.UseSourceAccess(func(context.Context, Actor, uint64) error { return denied })
	if _, err := service.CreateGrant(context.Background(), actor, 7, GrantCommand{IdempotencyKey: "original"}); !errors.Is(err, denied) {
		t.Fatal("grant ignored source ACL", err)
	}
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, "token", DownloadMetadata{}); !errors.Is(err, denied) {
		t.Fatal("download ignored current ACL", err)
	}
}
