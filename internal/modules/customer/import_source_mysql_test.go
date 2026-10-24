package customer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 仅允许显式配置的隔离测试数据库。调用方
// 必须应用包含 000120 的 CRM 迁移计划；所有夹具均回滚。
func TestImportPreviewSourceAndAuditAreAtomicOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required; isolated MySQL not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	for _, failAudit := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed intent", true: "audit rollback"}[failAudit], func(t *testing.T) {
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { _ = tx.Rollback().Error })
			tenant := "qa-import-source-" + time.Now().UTC().Format("20060102150405.000000000")
			repo := NewGORMRepository(tx)
			var writer audit.Writer = audit.NewGORMWriter(tx)
			if failAudit {
				writer = mergeAuditFake{err: errors.New("audit persistence unavailable")}
			}
			svc := NewService(tx, repo, writer, profileCodec(t)).UseOwnerDirectory(importTestCatalog())
			gateway := &importSideEffectGateway{}
			svc.UseImportFileGateway(gateway)
			content, err := customerImportTemplateWorkbook()
			if err != nil {
				t.Fatal(err)
			}
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "a"})
			preview, err := svc.PreviewImport(ctx, content, "isolated source lifecycle verification")
			if failAudit && err == nil {
				t.Fatal("audit failure did not reject preview")
			}
			if !failAudit && err != nil {
				t.Fatal(err)
			}
			want := int64(1)
			if failAudit {
				want = 0
			}
			for _, model := range []any{&ImportJob{}, &ImportRow{}, &ImportSource{}, &audit.Event{}} {
				var count int64
				if err := tx.Model(model).Where("tenant_id=?", tenant).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != want {
					t.Fatalf("atomic preview model=%T count=%d want=%d", model, count, want)
				}
			}
			if gateway.calls != 0 {
				t.Fatal("preview transaction made an external gateway call")
			}
			if !failAudit {
				var source ImportSource
				if err := tx.Where("tenant_id=?", tenant).Take(&source).Error; err != nil {
					t.Fatal(err)
				}
				plain, err := svc.codec.Decrypt(source.ContentCipher)
				if err != nil || !bytes.Equal([]byte(plain), content) || source.JobNo != preview.JobNo || source.Status != "PENDING" || source.FileID != "" {
					t.Fatal("preview source was not recoverable from encrypted durable intent")
				}
				// 验证真实租户谓词及比较并设置的并发隔离条件。
				now := time.Now().UTC()
				if claimed, _, err := repo.ClaimImportSource(ctx, "another-tenant", "wrong", now, now.Add(time.Minute)); err != nil || claimed != nil {
					t.Fatalf("cross-tenant source claim=%+v error=%v", claimed, err)
				}
				claimed, _, err := repo.ClaimImportSource(ctx, tenant, "worker-1", now, now.Add(time.Minute))
				if err != nil || claimed == nil {
					t.Fatalf("claim=%+v error=%v", claimed, err)
				}
				claimed.FileID, claimed.Status = "file-1", "UPLOADED"
				if err := repo.SaveImportSource(ctx, claimed, "old-worker", now); !errors.Is(err, ErrImportJobConflict) {
					t.Fatalf("stale worker was not fenced: %v", err)
				}
				if err := repo.SaveImportSource(ctx, claimed, "worker-1", now); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
