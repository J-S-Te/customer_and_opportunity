package account

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestDisableLinkTransactionAndBusinessIdempotency runs against an explicitly
// supplied disposable MySQL schema. It exercises the real GORM transaction;
// the normal unit suite skips it instead of silently emulating MySQL behavior.
func TestDisableLinkTransactionAndBusinessIdempotency(t *testing.T) {
	db := openPortalAccountTestDatabase(t)
	for _, model := range []any{&identityDisableOperation{}, &AuthEvent{}, &Session{}, &IdentityLink{}} {
		_ = db.Migrator().DropTable(model)
	}
	t.Cleanup(func() {
		for _, model := range []any{&identityDisableOperation{}, &AuthEvent{}, &Session{}, &IdentityLink{}} {
			_ = db.Migrator().DropTable(model)
		}
	})
	if err := db.AutoMigrate(&IdentityLink{}, &Session{}, &AuthEvent{}, &identityDisableOperation{}); err != nil {
		t.Fatalf("create disposable schema: %v", err)
	}

	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	link := IdentityLink{Model: newModel("tenant-a", "seed", now.Add(-time.Hour)), AccountNo: "PA-1", PlatformUserID: "subject-a", CustomerID: 7, Status: IdentityActive}
	if err := db.Create(&link).Error; err != nil {
		t.Fatalf("seed identity link: %v", err)
	}
	session := Session{Model: newModel("tenant-a", "subject-a", now.Add(-time.Hour)), PublicID: "session-public", SessionIDHash: "session-hash", PlatformUserID: "subject-a", CustomerID: 7, AuthzRevision: 1, RoleConfigHash: "catalog", Roles: []string{"portal_customer"}, Permissions: []string{"project.read"}, AccessTokenCipher: []byte("cipher"), AuthorizationCheckedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiry: now.Add(time.Hour), LastSeenAt: now}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	repo := NewGORMRepository(db)
	ctx := requestctx.WithID(context.Background(), "disable-request-1")
	command := DisableCommand{TenantID: "tenant-a", CustomerID: 7, PlatformUserID: "subject-a", ActorID: "machine:crm-portal-disable", Reason: "customer administrator revoked access", IdempotencyKey: "disable-operation-1"}

	// Removing the audit sink forces the final insert to fail. Identity/session
	// changes and the operation ledger must all roll back with that failure.
	if err := db.Migrator().DropTable(&AuthEvent{}); err != nil {
		t.Fatalf("drop audit table: %v", err)
	}
	if _, err := repo.DisableLink(ctx, command, now); err == nil {
		t.Fatal("disable unexpectedly succeeded without the required audit sink")
	}
	assertDisableDatabaseState(t, db, link.ID, session.ID, IdentityActive, false, 0, 0)
	if err := db.AutoMigrate(&AuthEvent{}); err != nil {
		t.Fatalf("restore audit table: %v", err)
	}

	result, err := repo.DisableLink(ctx, command, now)
	if err != nil || result.Status != IdentityDisabled || result.CustomerID != 7 || result.PlatformUserID != "subject-a" || result.Version != link.Version+1 {
		t.Fatalf("DisableLink() result=%#v err=%v", result, err)
	}
	assertDisableDatabaseState(t, db, link.ID, session.ID, IdentityDisabled, true, 1, 1)

	replay, err := repo.DisableLink(requestctx.WithID(context.Background(), "disable-request-retry"), command, now.Add(time.Minute))
	if err != nil || replay != result {
		t.Fatalf("exact replay result=%#v err=%v want=%#v", replay, err, result)
	}
	assertDisableDatabaseState(t, db, link.ID, session.ID, IdentityDisabled, true, 1, 1)

	conflict := command
	conflict.Reason = "different payload"
	if _, err = repo.DisableLink(ctx, conflict, now); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	wrongCustomer := command
	wrongCustomer.IdempotencyKey = "disable-operation-2"
	wrongCustomer.CustomerID = 8
	if _, err = repo.DisableLink(ctx, wrongCustomer, now); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("customer/subject mismatch error=%v", err)
	}
	wrongSubject := command
	wrongSubject.IdempotencyKey = "disable-operation-3"
	wrongSubject.PlatformUserID = "subject-b"
	if _, err = repo.DisableLink(ctx, wrongSubject, now); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("subject/customer mismatch error=%v", err)
	}
	wrongActor := command
	wrongActor.ActorID = "machine:another-client"
	if _, err = repo.DisableLink(ctx, wrongActor, now); !errors.Is(err, ErrIdentityDisabled) {
		t.Fatalf("different client cannot replay an already-disabled mapping: error=%v", err)
	}
}

func TestTouchSessionAcceptsMySQLNoChangeAndRejectsInactiveRows(t *testing.T) {
	db := openPortalAccountTestDatabase(t)
	if strings.Contains(strings.ToLower(os.Getenv("PORTAL_ACCOUNT_TEST_MYSQL_DSN")), "clientfoundrows=true") {
		t.Fatal("PORTAL_ACCOUNT_TEST_MYSQL_DSN must use clientFoundRows=false to exercise MySQL zero-change semantics")
	}
	_ = db.Migrator().DropTable(&Session{})
	t.Cleanup(func() { _ = db.Migrator().DropTable(&Session{}) })
	if err := db.AutoMigrate(&Session{}); err != nil {
		t.Fatalf("create disposable session table: %v", err)
	}

	now := time.Date(2026, 8, 3, 9, 10, 11, 123_000_000, time.UTC)
	session := Session{
		Model:                  newModel("tenant-a", "subject-a", now),
		PublicID:               "touch-public",
		SessionIDHash:          "touch-session-hash",
		PlatformUserID:         "subject-a",
		CustomerID:             7,
		AuthzRevision:          1,
		RoleConfigHash:         "catalog",
		Roles:                  []string{"portal_customer"},
		Permissions:            []string{"project.read"},
		AccessTokenCipher:      []byte("cipher"),
		AuthorizationCheckedAt: now,
		ExpiresAt:              now.Add(time.Hour),
		AbsoluteExpiry:         now.Add(time.Hour),
		LastSeenAt:             now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	repo := NewGORMRepository(db)
	noChange := db.Model(&Session{}).
		Where("tenant_id = ? AND session_id_hash = ?", "tenant-a", session.SessionIDHash).
		Updates(map[string]any{"last_seen_at": now, "updated_at": now, "authorization_checked_at": now})
	if noChange.Error != nil {
		t.Fatalf("verify MySQL changed-row behavior: %v", noChange.Error)
	}
	if noChange.RowsAffected != 0 {
		t.Fatalf("no-change UPDATE affected %d rows; test database must use MySQL changed-row semantics (clientFoundRows=false)", noChange.RowsAffected)
	}

	// Every value in the UPDATE already equals its DATETIME(3) database value,
	// so MySQL reports zero changed rows. The authoritative recheck must still
	// accept the active session, including on a repeated sibling request.
	for attempt := 0; attempt < 2; attempt++ {
		if err := repo.TouchSession(context.Background(), "tenant-a", session.SessionIDHash, now, now); err != nil {
			t.Fatalf("TouchSession() attempt %d rejected an active no-change row: %v", attempt+1, err)
		}
	}

	if err := db.Model(&Session{}).Where("id = ?", session.ID).Update("revoked_at", now).Error; err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if err := repo.TouchSession(context.Background(), "tenant-a", session.SessionIDHash, now, now); !errors.Is(err, ErrInvalidLoginState) {
		t.Fatalf("TouchSession() revoked error = %v, want ErrInvalidLoginState", err)
	}
}

func openPortalAccountTestDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("PORTAL_ACCOUNT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("PORTAL_ACCOUNT_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open disposable MySQL schema: %v", err)
	}
	return db
}

func assertDisableDatabaseState(t *testing.T, db *gorm.DB, linkID, sessionID uint64, status IdentityStatus, revoked bool, operations, audits int64) {
	t.Helper()
	var link IdentityLink
	if err := db.First(&link, linkID).Error; err != nil || link.Status != status {
		t.Fatalf("identity link status=%q err=%v want=%q", link.Status, err, status)
	}
	var session Session
	if err := db.First(&session, sessionID).Error; err != nil || (session.RevokedAt != nil) != revoked {
		t.Fatalf("session revoked=%v err=%v want=%v", session.RevokedAt != nil, err, revoked)
	}
	var operationCount, auditCount int64
	if err := db.Table((identityDisableOperation{}).TableName()).Count(&operationCount).Error; err != nil {
		t.Fatalf("count disable operations: %v", err)
	}
	if db.Migrator().HasTable(&AuthEvent{}) {
		if err := db.Model(&AuthEvent{}).Count(&auditCount).Error; err != nil {
			t.Fatalf("count audit events: %v", err)
		}
	}
	if operationCount != operations || auditCount != audits {
		t.Fatalf("operation/audit counts=%d/%d want=%d/%d", operationCount, auditCount, operations, audits)
	}
}
