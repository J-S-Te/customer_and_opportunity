package crmauth_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/credit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/crmauth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestSettlementCreditEventOverHTTP 验证真实 CRM 内部路由、
// 机器 JWT 校验、nonce/时间戳防重放、信用服务
// 及 MySQL 记录。需迁移后的临时 CRM 数据库，因此显式启用。
func TestSettlementCreditEventOverHTTP(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_CREDIT_TEST_DSN"))
	privateKeyPath := strings.TrimSpace(os.Getenv("CRM_CREDIT_PRIVATE_KEY"))
	publicKeyPath := strings.TrimSpace(os.Getenv("CRM_CREDIT_PUBLIC_KEY"))
	if dsn == "" || privateKeyPath == "" || publicKeyPath == "" {
		t.Skip("CRM_CREDIT_TEST_DSN, CRM_CREDIT_PRIVATE_KEY and CRM_CREDIT_PUBLIC_KEY are required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	const tenant = "tenant-http-e2e"
	now := time.Now().UTC()
	if err = db.Exec(`INSERT INTO crm_customers
(id,tenant_id,customer_no,name,normalized_name,customer_type,industry,region,owner_user_id,owner_org_id,status,created_by,updated_by,created_at,updated_at)
VALUES(42,?,?,?,?,?,?,?,?,?, 'ACTIVE','e2e','e2e',?,?) ON DUPLICATE KEY UPDATE status='ACTIVE'`,
		tenant, "C-42", "HTTP E2E 客户", "http e2e 客户", "ENTERPRISE", "IT", "杭州", "e2e", "", now, now).Error; err != nil {
		t.Fatal(err)
	}
	authn, err := crmauth.NewMachineAuthenticator(context.Background(), db, crmauth.MachineOptions{
		Issuer: "basic-platform", Audience: "basic-platform-application", PublicKeyPath: publicKeyPath, TenantID: tenant,
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	internal := router.Group("/customer-opportunity/api/v1/internal")
	internal.Use(middleware.MachineAuth(authn))
	credit.RegisterInternalRoutes(internal, credit.NewHandler(credit.NewService(db)))
	server := httptest.NewServer(router)
	defer server.Close()

	token := signApplicationJWT(t, privateKeyPath, tenant, now)
	payload := []byte(`{"event_id":"http-credit-1","event_type":"customer.credit.payment.completed.v1","payment_id":"http-allocation-1","customer_id":42,"contract_no":"HT-HTTP-1","period_no":"1","due_date":"2099-12-31T00:00:00Z","paid_date":"2026-10-18T00:00:00Z","due_amount":"100.00","paid_amount":"100.00","source_system":"settlement"}`)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/customer-opportunity/api/v1/internal/credit/payment-events", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 24)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Integration-Timestamp", now.Format(time.RFC3339Nano))
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var body bytes.Buffer
		_, _ = body.ReadFrom(response.Body)
		t.Fatalf("HTTP status=%d body=%s", response.StatusCode, body.String())
	}
	var count int64
	if err = db.Table("crm_customer_credit_payment_records").Where("tenant_id=? AND event_id=?", tenant, "http-credit-1").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("credit record count=%d err=%v", count, err)
	}
}

func signApplicationJWT(t *testing.T, privateKeyPath, tenant string, now time.Time) string {
	t.Helper()
	contents, err := os.ReadFile(privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(contents)
	if block == nil {
		t.Fatal("private key PEM is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		t.Fatal("private key is not Ed25519")
	}
	header := map[string]any{"alg": "EdDSA", "typ": "JWT"}
	claims := map[string]any{
		"iss": "basic-platform", "aud": "basic-platform-application", "token_use": "application",
		"sub": "settlement-credit-publisher", "oauth_client_id": "client-record-id",
		"tenant_id": tenant, "application_id": "app-id", "application_code": "settlement",
		"environment_id": "env-id", "environment_code": "dev", "scope": []string{"customer.credit.payment.ingest"},
		"iat": now.Add(-time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
	headerBytes, _ := json.Marshal(header)
	claimBytes, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimBytes)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(signingInput)))
}
