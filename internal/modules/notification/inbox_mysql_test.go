package notification

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Explicit fixture DSN only: inherited business configuration is never used.
// The owning test launcher first applies the actual CRM migration plan to a
// new schema inside its temporary MySQL container.
func TestPlatformUserInboxAgainstIsolatedMySQL(t *testing.T) {
	raw := os.Getenv("CRM_INBOX_TEST_DSN")
	if raw == "" {
		t.Skip("CRM_INBOX_TEST_DSN is required; no connection attempted")
	}
	cfg, err := driver.ParseDSN(raw)
	if err != nil || cfg == nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.DBName, "qa_review_") {
		t.Fatal("requires an explicitly owned qa_review_ fixture schema")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("fixture must be loopback-only")
	}
	db, err := gorm.Open(mysql.Open(raw), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot open isolated inbox fixture")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	const tenant, user = "qa-inbox-identity", "qa-user"
	var preexisting int64
	if err := db.Model(&Notification{}).Count(&preexisting).Error; err != nil || preexisting != 0 {
		t.Fatal("requires a freshly migrated empty notification table")
	}
	now := time.Now().UTC()
	messages := []Notification{}
	for i, recipient := range []string{user, "other-user", "foreign-pms-person"} {
		messages = append(messages, Notification{
			Model:         database.Model{TenantID: tenant, CreatedBy: "fixture", UpdatedBy: "fixture", CreatedAt: now, UpdatedAt: now, Version: 1},
			SourceEventID: "qa-inbox-" + recipient, Type: TypePresaleAssigneeAdded,
			RecipientID: recipient, RecipientNamespace: "USER", RecipientKind: RecipientAssigneeAdded,
			Title: "fixture", Body: "fixture", Status: StatusUnread,
			RequestID: uint64(i + 1), RequestNo: "QA-PRESALE-" + recipient,
			AssignmentID: uint64(i + 1), TargetPath: "/customer-opportunity/presale",
		})
	}
	if err := db.Create(&messages).Error; err != nil {
		t.Fatalf("seed migrated notification table: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Where("tenant_id = ? AND source_event_id LIKE ?", tenant, "qa-inbox-%").Unscoped().Delete(&Notification{}).Error; err != nil {
			t.Errorf("clean fixture notifications: %v", err)
		}
	})
	service := NewService(db)
	for _, person := range []string{"", "foreign-pms-person"} {
		ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: user, PersonID: person, Permissions: map[string]struct{}{"presale.read": {}}})
		page, err := service.ListMine(ctx, false, 1, 20)
		if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != messages[0].ID {
			t.Fatalf("user inbox crossed identity boundary: total=%d err=%v", page.Total, err)
		}
		if err := service.MarkRead(ctx, messages[1].ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign user notification was readable: %v", err)
		}
		if err := service.MarkRead(ctx, messages[2].ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("PMS identity was treated as platform user: %v", err)
		}
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: user, Permissions: map[string]struct{}{"presale.read": {}}})
	if count, err := service.UnreadCount(ctx); err != nil || count != 1 {
		t.Fatalf("unread count=%d err=%v", count, err)
	}
	for i := 0; i < 2; i++ {
		if err := service.MarkRead(ctx, messages[0].ID); err != nil {
			t.Fatalf("mark-read replay failed: %v", err)
		}
	}
	if count, err := service.UnreadCount(ctx); err != nil || count != 0 {
		t.Fatalf("unread after mark-read=%d err=%v", count, err)
	}
}
