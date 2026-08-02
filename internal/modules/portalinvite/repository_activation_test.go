package portalinvite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type activationSQLRecorder struct {
	statement string
}

func (r *activationSQLRecorder) LogMode(logger.LogLevel) logger.Interface { return r }
func (*activationSQLRecorder) Info(context.Context, string, ...any)       {}
func (*activationSQLRecorder) Warn(context.Context, string, ...any)       {}
func (*activationSQLRecorder) Error(context.Context, string, ...any)      {}
func (r *activationSQLRecorder) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	r.statement, _ = query()
}

func TestActivateIdentityLinkSQLIsPendingOnlyAndFullyInviteBound(t *testing.T) {
	recorder := &activationSQLRecorder{}
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: recorder})
	if err != nil {
		t.Fatal(err)
	}

	repo := NewGORMRepository(db)
	invite := &Invite{
		Model:           database.Model{TenantID: "tenant-a"},
		CustomerID:      7,
		ContactID:       9,
		PlatformUserID:  "subject-a",
		PortalAccountID: "portal-account-a",
	}
	link := &IdentityLink{
		Model:           database.Model{ID: 11, TenantID: "tenant-a", Version: 3},
		CustomerID:      7,
		ContactID:       9,
		PlatformUserID:  "subject-a",
		PortalAccountID: "portal-account-a",
		Status:          StatusPending,
	}

	err = repo.ActivateIdentityLink(context.Background(), invite, link, "portal-machine", time.Now().UTC())
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("dry-run update should have no affected row, got %v", err)
	}
	sql := strings.ToLower(recorder.statement)
	for _, fragment := range []string{
		"update `crm_portal_identity_links`",
		"`status`='active'",
		"id = 11",
		"version = 3",
		"tenant_id = 'tenant-a'",
		"customer_id = 7",
		"contact_id = 9",
		"platform_user_id = 'subject-a'",
		"portal_account_id = 'portal-account-a'",
		"status = 'pending'",
		"deleted_at is null",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("activation SQL missing %q: %s", fragment, recorder.statement)
		}
	}
}
