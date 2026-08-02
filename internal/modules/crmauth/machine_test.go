package crmauth

import (
	"errors"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestMachineClaimsFollowPlatformApplicationTokenContract(t *testing.T) {
	// Platform application_jwt.go intentionally issues sub=client_id and
	// oauth_client_id=registry row ID; equality would reject every real token.
	valid := machineClaims{Subject: "quote-production", OAuthClientID: "01J-OAUTH-ROW", TenantID: "tenant-1", TokenUse: "application", Scopes: []string{"opportunity.status.write"}}
	if err := validateMachineClaims(valid, "tenant-1"); err != nil {
		t.Fatalf("valid platform claims rejected: %v", err)
	}
	portal := valid
	portal.Scopes = []string{"portal.invite.verify", "customer.summary.read"}
	if err := validateMachineClaims(portal, "tenant-1"); err != nil {
		t.Fatalf("documented Portal-to-CRM scopes rejected: %v", err)
	}
	scanner := valid
	scanner.Scopes = []string{"opportunity.attachment.scan.write"}
	if err := validateMachineClaims(scanner, "tenant-1"); err != nil {
		t.Fatalf("attachment scanner callback scope rejected: %v", err)
	}
	tests := []machineClaims{
		{Subject: "", OAuthClientID: valid.OAuthClientID, TenantID: valid.TenantID, TokenUse: valid.TokenUse, Scopes: valid.Scopes},
		{Subject: valid.Subject, OAuthClientID: "", TenantID: valid.TenantID, TokenUse: valid.TokenUse, Scopes: valid.Scopes},
		{Subject: valid.Subject, OAuthClientID: valid.OAuthClientID, TenantID: "other", TokenUse: valid.TokenUse, Scopes: valid.Scopes},
		{Subject: valid.Subject, OAuthClientID: valid.OAuthClientID, TenantID: valid.TenantID, TokenUse: "access_token", Scopes: valid.Scopes},
		{Subject: valid.Subject, OAuthClientID: valid.OAuthClientID, TenantID: valid.TenantID, TokenUse: valid.TokenUse, Scopes: []string{"contract.read"}},
	}
	for index, claims := range tests {
		if err := validateMachineClaims(claims, "tenant-1"); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("invalid claims[%d] error = %v", index, err)
		}
	}
}

func TestDuplicateReplayRecognizesGORMAndRawMySQL1062(t *testing.T) {
	if !isDuplicateReplay(gorm.ErrDuplicatedKey) {
		t.Fatal("GORM duplicate was not recognized")
	}
	if !isDuplicateReplay(&mysqlDriver.MySQLError{Number: 1062, Message: "duplicate"}) {
		t.Fatal("raw MySQL 1062 was not recognized")
	}
	if isDuplicateReplay(&mysqlDriver.MySQLError{Number: 1205, Message: "lock timeout"}) {
		t.Fatal("non-duplicate MySQL error was misclassified")
	}
}
