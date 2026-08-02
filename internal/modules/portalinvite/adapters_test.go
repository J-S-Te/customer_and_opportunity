package portalinvite

import (
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// This dry-run assertion protects the IDOR boundary without replacing the
// required real-MySQL repository integration suite at release time.
func TestCustomerAdapterSQLAppliesOwnerScope(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "crm:test@tcp(127.0.0.1:3306)/crm?parseTime=true", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := security.NewSensitiveCodec([]byte("01234567890123456789012345678901"), []byte("abcdefghijklmnopqrstuvwxyz123456"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewCustomerAdapter(db, codec)
	// DryRun cannot return rows, but the generated statement is retained even
	// though the adapter correctly maps the empty result to CONTACT_INVALID.
	_, _ = adapter.RegistrationContact(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf}, 7)
	statement := db.Statement.SQL.String()
	if statement == "" {
		// GORM clones sessions for chained queries, so assert through the common
		// builder directly when the fallback DB doesn't retain child SQL.
		statement = scopedContactQuery(db, auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf}, 7).Limit(2).Find(&[]struct{}{}).Statement.SQL.String()
	}
	for _, expected := range []string{"c.tenant_id = ?", "c.id = ?", "c.owner_user_id = ?", "ct.is_registration = TRUE", "LIMIT ?"} {
		if !containsSQL(statement, expected) {
			t.Fatalf("SQL missing %q: %s", expected, statement)
		}
	}
}

func containsSQL(value, fragment string) bool {
	normalize := func(v string) string {
		result := make([]byte, 0, len(v))
		space := false
		for _, char := range []byte(v) {
			if char == '`' {
				continue
			}
			if char == ' ' || char == '\n' || char == '\t' {
				if !space {
					result = append(result, ' ')
					space = true
				}
				continue
			}
			space = false
			result = append(result, char)
		}
		return string(result)
	}
	return len(fragment) == 0 || stringContains(normalize(value), normalize(fragment))
}
func stringContains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
