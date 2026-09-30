package crmauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
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
	contract := valid
	contract.Scopes = []string{"customer.contract_reference.read"}
	if err := validateMachineClaims(contract, "tenant-1"); err != nil {
		t.Fatalf("contract reference scope rejected: %v", err)
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

type machineTestJWTHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

// machineTestJWTPayload 按 platform application_jwt.go 的应用令牌契约复刻载荷字段；
// 验签器使用严格 JSON 解码，字段集合必须与平台签发完全一致。
type machineTestJWTPayload struct {
	Issuer          string   `json:"iss"`
	Audience        string   `json:"aud"`
	TokenUse        string   `json:"token_use"`
	Subject         string   `json:"sub"`
	OAuthClientID   string   `json:"oauth_client_id"`
	TenantID        string   `json:"tenant_id"`
	ApplicationID   string   `json:"application_id"`
	ApplicationCode string   `json:"application_code"`
	EnvironmentID   string   `json:"environment_id"`
	EnvironmentCode string   `json:"environment_code"`
	Scopes          []string `json:"scope"`
	IssuedAt        int64    `json:"iat"`
	NotBefore       int64    `json:"nbf"`
	ExpiresAt       int64    `json:"exp"`
}

func signMachineTestToken(t *testing.T, privateKey ed25519.PrivateKey, payload machineTestJWTPayload) string {
	t.Helper()
	headerBytes, err := json.Marshal(machineTestJWTHeader{Algorithm: "EdDSA", Type: "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(signingInput)))
}

func newMachineTestAuthenticator(t *testing.T, now time.Time) (*MachineAuthenticator, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "application-jwt-public.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded}), 0o400); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	authenticator, err := NewMachineAuthenticator(t.Context(), db, MachineOptions{
		Issuer: "basic-platform", Audience: "basic-platform-application", PublicKeyPath: path, TenantID: "tenant-1",
	})
	if err != nil {
		t.Fatalf("NewMachineAuthenticator() error = %v", err)
	}
	authenticator.now = func() time.Time { return now }
	return authenticator, privateKey
}

// 平台为合同系统签发的 opportunity.signed.write 机器令牌必须能通过 Authenticate，
// 否则 internal /opportunities/:id/contract-link 签约回链端点在验签层就 401，签约回写闭环断裂。
func TestMachineAuthenticateAdmitsContractSignedWriteScope(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	authenticator, privateKey := newMachineTestAuthenticator(t, now)
	request := func(token string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/opportunities/9/contract-link", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-Integration-Timestamp", now.Format(time.RFC3339Nano))
		request.Header.Set("X-Integration-Nonce", "nonce-contract-link")
		return request
	}
	base := machineTestJWTPayload{
		Issuer: "basic-platform", Audience: "basic-platform-application", TokenUse: "application",
		Subject: "crm-contract-opportunity-intake", OAuthClientID: "01J00000000000000000000001", TenantID: "tenant-1",
		ApplicationID: "01J00000000000000000000002", ApplicationCode: "crm", EnvironmentID: "01J00000000000000000000003",
		EnvironmentCode: "prod", Scopes: []string{"opportunity.signed.write"},
		IssuedAt: now.Add(-time.Minute).Unix(), NotBefore: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(10 * time.Minute).Unix(),
	}
	principal, err := authenticator.Authenticate(t.Context(), request(signMachineTestToken(t, privateKey, base)))
	if err != nil {
		t.Fatalf("Authenticate() with opportunity.signed.write scope error = %v", err)
	}
	if _, ok := principal.Permissions["opportunity.signed.write"]; !ok {
		t.Fatalf("principal permissions = %#v, want opportunity.signed.write", principal.Permissions)
	}
	if principal.TenantID != "tenant-1" || len(principal.Roles) != 1 || principal.Roles[0] != "machine" {
		t.Fatalf("unexpected machine principal: %#v", principal)
	}

	// scope 全部不在白名单时仍必须 401（fail-closed），白名单扩充不能引入放行面。
	unknown := base
	unknown.Scopes = []string{"contract.read"}
	if _, err = authenticator.Authenticate(t.Context(), request(signMachineTestToken(t, privateKey, unknown))); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate() with unknown scope error = %v, want ErrUnauthenticated", err)
	}
}
