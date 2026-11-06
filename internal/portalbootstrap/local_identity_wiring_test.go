package portalbootstrap

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/capability"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

// validLocalConfig 构造不依赖CRM的完整本地启动材料。
// 参数：无。
// 返回值：仅合成凭据，不启动外部进程。
func validLocalConfig() Config {
	c := validPortalConfig()
	c.LocalDirectoryMode = "local"
	c.LocalInviteHMACKey = bytes.Repeat([]byte{8}, 32)
	c.LocalPlatformTokenURL = "https://identity.example/oauth2/token"
	credential := func(id string) LocalIdentityCredential {
		return LocalIdentityCredential{ClientID: id, ClientSecret: "fixture-only"}
	}
	c.LocalProvision = credential("local-provision")
	c.LocalRoleAssign = credential("local-role-assign")
	c.LocalRoleRevoke = credential("local-role-revoke")
	c.LocalBindingWrite = credential("local-bind")
	c.LocalBindingDisable = credential("local-disable")
	c.FileGatewayBaseURL = "https://gateway.example"
	c.FileGatewayClientID = "local-file-writer"
	c.FileGatewayClientSecret = "fixture"
	c.FileGatewayScope = "platform:file:upload platform:file:download"
	c.FileGatewayApplicationID = "portal-application"
	c.ReportSourceApplicationID = "portal-application"
	c.ReportContentKey = bytes.Repeat([]byte{9}, 32)
	c.ReportStoreRoot = "/tmp/portal-local-vault"
	c.PDFConfigRoot = "/tmp/portal-local-pdf"
	c.CJKFontPath = "/fixture/font.ttf"
	c.CRMProvisionClientSubject = ""
	c.CRMDisableClientSubject = ""
	c.CRMInviteBaseURL = ""
	c.CRMInviteTokenURL = ""
	c.CRMInviteClientID = ""
	c.CRMInviteClientSecret = ""
	c.CRMInviteScope = ""
	c.ReportIdentityURL = ""
	c.ReportIdentityClientID = ""
	c.ReportIdentityClientSecret = ""
	return c
}

// TestLocalConfigRequiresRealIndependentDependencies 验证默认兼容且本地缺依赖无法启动。
// 参数：t为测试上下文。
// 返回值：无；缺依赖仍通过、允许CRM混合或密钥复用时失败。
func TestLocalConfigRequiresRealIndependentDependencies(t *testing.T) {
	if err := validPortalConfig().validate(); err != nil {
		t.Fatal("CRM default regressed", err)
	}
	c := validLocalConfig()
	if err := c.validate(); err != nil {
		t.Fatal("complete local config rejected", err)
	}
	for name, mutate := range map[string]func(*Config){"unknown-mode": func(c *Config) { c.LocalDirectoryMode = "LOCAL" }, "missing-hmac": func(c *Config) { c.LocalInviteHMACKey = nil }, "reused-key": func(c *Config) { c.LocalInviteHMACKey = c.EncryptionKey }, "missing-role-secret": func(c *Config) { c.LocalRoleAssign.ClientSecret = "" }, "mixed-client": func(c *Config) { c.LocalBindingWrite = c.LocalProvision }, "missing-vault": func(c *Config) { c.ReportStoreRoot = "" }, "wrong-app": func(c *Config) { c.ReportSourceApplicationID = "other" }, "local-files": func(c *Config) { c.FileGatewayLocalEnabled = true }, "invalid-token": func(c *Config) { c.LocalPlatformTokenURL = "https://user:secret@identity.example/token" }} {
		t.Run(name, func(t *testing.T) {
			bad := validLocalConfig()
			mutate(&bad)
			if err := bad.validate(); err == nil {
				t.Fatal("invalid local dependency accepted")
			}
		})
	}
	c.LocalProvision.ClientSecret = ""
	if _, err := New(context.Background(), c); err == nil || !strings.Contains(err.Error(), "PORTAL_LOCAL_PROVISION") {
		t.Fatalf("New did not reject before license/DB/OIDC: %v", err)
	}
	t.Setenv("PORTAL_CUSTOMER_DIRECTORY_MODE", "true")
	if err := loadLocalIdentityConfig(&Config{}); err == nil {
		t.Fatal("unknown environment mode accepted")
	}
}

type localCapabilityFailure struct{ calls int }

// Get 故意拒绝零客户查功能，证明管理元数据的精确豁免。
// 参数：请求、租户和客户。
// 返回值：依赖失败。
func (f *localCapabilityFailure) Get(context.Context, string, uint64) (capability.Options, error) {
	f.calls++
	return capability.Options{}, errors.New("no customer capabilities for admin")
}

// localAdminAccount 构造已通过平台授权复核的管理会话替身。
// 参数：t为测试，customer表示是否伪装客户身份。
// 返回值：原账户服务，仍执行真实会话认证规则。
func localAdminAccount(t *testing.T, customer bool) *account.Service {
	t.Helper()
	now := time.Now().UTC()
	role := "portal_super_admin"
	customerID := uint64(0)
	permissions := []string{localidentity.PermissionCustomerManage, "project.read", "report.download", "account.security.manage"}
	if customer {
		role = "portal_customer"
		customerID = 7
		permissions = []string{"project.read"}
	}
	codec, err := NewAEADCodec(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := codec.Encrypt([]byte("access-token"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &localMetadataRepository{reportNotificationRouteAccountRepository: reportNotificationRouteAccountRepository{session: &account.Session{Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: reportNotificationRouteSessionHash("opaque-session"), PlatformUserID: "subject-a", CustomerID: customerID, Roles: []string{role}, Permissions: permissions, DataScopes: []account.DataScope{{RoleCode: role, ScopeType: "APPLICATION"}}, AuthorizationCheckedAt: now, AccessTokenCipher: cipher, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, link: &account.IdentityLink{Model: database.Model{TenantID: "tenant-a"}, CustomerID: customerID, PlatformUserID: "subject-a", Status: account.IdentityActive}}}
	return account.NewService(repo, nil, nil, &SecretProtector{codec: codec}, account.SystemClock{}, account.CryptoRandom{}, "catalog", time.Hour)
}

// TestLocalAdminMetadataDoesNotGrantCustomerBusiness 验证零客户管理员只进入管理及元数据。
// 参数：t为测试上下文。
// 返回值：无；跳过原业务ACL、允许客户管理或漏CSRF时失败。
func TestLocalAdminMetadataDoesNotGrantCustomerBusiness(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	caps := &localCapabilityFailure{}
	config := Config{LocalDirectoryMode: "local", TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	router := NewRouter(RouterDependencies{Config: config, Account: localAdminAccount(t, false), CustomerCapabilities: caps})
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v1/auth/me", 200}, {"GET", "/api/v1/account/sessions", 200}, {"GET", "/api/v1/capabilities", 200}, {"GET", "/api/v1/projects", 403}, {"POST", "/api/v1/reports/1/downloads", 403}, {"GET", "/api/v1/local-admin/customers", 503}, {"POST", "/api/v1/local-admin/customers", 403}} {
		request := httptest.NewRequest(tc.method, config.PathPrefix+tc.path, strings.NewReader(`{}`))
		request.AddCookie(&http.Cookie{Name: config.SessionCookieName, Value: "opaque-session"})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		if w.Code != tc.status {
			t.Fatalf("%s %s status %d want %d: %s", tc.method, tc.path, w.Code, tc.status, w.Body.String())
		}
	}
	// 零客户管理身份不会查询客户功能；旧业务路由直接拒绝零客户。
	if caps.calls != 0 {
		t.Fatalf("metadata did not preserve exact customer intersection: %d", caps.calls)
	}
	router = NewRouter(RouterDependencies{Config: config, Account: localAdminAccount(t, true)})
	request := httptest.NewRequest("GET", config.PathPrefix+"/api/v1/local-admin/customers", nil)
	request.AddCookie(&http.Cookie{Name: config.SessionCookieName, Value: "opaque-session"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("customer reached local admin", w.Code)
	}
}

type localMetadataRepository struct {
	reportNotificationRouteAccountRepository
}

// ListSessions 仅返回当前平台主体的合成会话，验证零客户自身安全路由。
// 参数：ctx/tenant/subject为原账户服务传入的精确本人坐标，now为时钟。
// 返回值：有界合成会话，不关联客户业务。
func (r *localMetadataRepository) ListSessions(_ context.Context, tenant, subject string, _ time.Time) ([]account.Session, error) {
	if tenant != r.session.TenantID || subject != r.session.PlatformUserID {
		return nil, errors.New("cross subject")
	}
	return []account.Session{*r.session}, nil
}
