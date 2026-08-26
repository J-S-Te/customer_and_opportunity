package portalinvite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type scopedCustomerReader struct{ identity ContactIdentity }

func (s scopedCustomerReader) RegistrationContact(_ context.Context, principal auth.Principal, _ uint64) (ContactIdentity, error) {
	if principal.UserID != "sales-a" {
		return ContactIdentity{}, ErrContactInvalid
	}
	return s.identity, nil
}

func TestPublicRouteRequiresPermissionAndCustomerScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, _, _ := newTestService(now)
	service.customers = scopedCustomerReader{identity: ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, Phone: "13800138000"}}
	handler := NewHandler(service)
	router := gin.New()
	api := router.Group("/api/v1", func(c *gin.Context) {
		permissions := map[string]struct{}{}
		if c.GetHeader("X-Test-Provision") == "true" {
			permissions["portal_account.provision"] = struct{}{}
		}
		principal := auth.Principal{TenantID: "tenant-a", UserID: "other-sales", ScopeMode: auth.ScopeSelf, Permissions: permissions}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	RegisterRoutes(api, handler)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/customers/7/portal-invites", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing permission status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/customers/7/portal-invites", nil)
	request.Header.Set("X-Test-Provision", "true")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("out-of-scope customer status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCreateResponseReturnsNonCacheableActivationURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, _, _ := newTestService(now)
	router := gin.New()
	api := router.Group("/api/v1", func(c *gin.Context) {
		principal := auth.Principal{
			TenantID:  "tenant-a",
			UserID:    "sales-a",
			ScopeMode: auth.ScopeAll,
			Permissions: map[string]struct{}{
				"portal_account.provision": {},
			},
		}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	RegisterRoutes(api, NewHandler(service))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/customers/7/portal-invites", nil)
	request.Header.Set("Idempotency-Key", "handler-create-contract")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store, private" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("sensitive response cache policy is missing: %#v", response.Header())
	}
	var body struct {
		Code string `json:"code"`
		Data struct {
			ActivationURL string `json:"activation_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "OK" || strings.TrimSpace(body.Data.ActivationURL) == "" {
		t.Fatal("create response must contain a one-time activation URL")
	}
}

func TestCurrentInviteRouteAllowsProvisionOrRevokeOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, _, _ := newTestService(now)
	service.customers = scopedCustomerReader{identity: ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, Phone: "13800138000"}}
	router := gin.New()
	api := router.Group("/api/v1", func(c *gin.Context) {
		permissions := map[string]struct{}{}
		if permission := c.GetHeader("X-Test-Permission"); permission != "" {
			permissions[permission] = struct{}{}
		}
		principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf, Permissions: permissions}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	RegisterRoutes(api, NewHandler(service))

	for _, test := range []struct {
		permission string
		want       int
	}{
		{permission: "portal_account.provision", want: http.StatusNotFound},
		{permission: "portal_account.revoke", want: http.StatusNotFound},
		{permission: "portal_account.disable", want: http.StatusForbidden},
		{permission: "customer.read", want: http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/customers/7/portal-invites/current", nil)
		request.Header.Set("X-Test-Permission", test.permission)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("permission=%s status=%d want=%d body=%s", test.permission, response.Code, test.want, response.Body.String())
		}
	}
}

func TestVerifyResponseUsesUnifiedEnvelopeWithoutTokenHash(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, _, _ := newTestService(now)
	created, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "handler-create-1"})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(created.ActivationURL, "https://portal.example/customer-portal/activate?token=")
	router := gin.New()
	router.POST("/verify", NewHandler(service).Verify)
	body, _ := json.Marshal(VerifyRequest{Token: token})
	request := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	text := response.Body.String()
	for _, forbidden := range []string{"token_hash", "created_by", "updated_by", "revoked_reason"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("verify response leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"platform_user_id":"platform-subject-123456789"`) {
		t.Fatalf("verify contract mismatch: %s", text)
	}
	if !strings.Contains(text, `"code":"OK"`) || !strings.Contains(text, `"data":{`) {
		t.Fatalf("verify did not use unified response envelope: %s", text)
	}
}

func TestConsumeResponseUsesUnifiedEnvelope(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, _, _ := newTestService(now)
	created, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "handler-create-2"})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(created.ActivationURL, "https://portal.example/customer-portal/activate?token=")
	router := gin.New()
	router.POST("/consume", NewHandler(service).Consume)
	body, _ := json.Marshal(ConsumeRequest{Token: token, PlatformUserID: "platform-subject-123456789", RequestID: "portal-request"})
	request := httptest.NewRequest(http.MethodPost, "/consume", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":"OK"`) {
		t.Fatalf("consume contract mismatch: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMachineJSONHandlersRejectUnsafeBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := map[string]string{
		"unknown field":    `{"token":"not-a-token","unexpected":"sensitive-input"}`,
		"trailing object":  `{"token":"not-a-token"}{"token":"second"}`,
		"empty body":       "",
		"missing required": `{}`,
		"over one MiB":     `{"token":"` + strings.Repeat("x", (1<<20)+1) + `"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			context.Request = request

			(&Handler{}).Verify(context)

			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"COMMON_INVALID_ARGUMENT"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "sensitive-input") || strings.Contains(recorder.Body.String(), "unknown field") {
				t.Fatalf("parser or request detail leaked: %s", recorder.Body.String())
			}
		})
	}
}
