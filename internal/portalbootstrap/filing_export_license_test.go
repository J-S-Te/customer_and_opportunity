package portalbootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/filing"
)

// 仅替换仓储和渲染器。请求经过真实的
// 商业授权中间件、签名与策略评估及导出服务。
func TestFilingExportCommercialLicenseBoundary(t *testing.T) {
	now := time.Date(2026, 10, 13, 4, 0, 0, 0, time.UTC)
	license := filingExportExpiredLicense(t, now)
	if err := core.Evaluate(license, "customer_portal", core.MUTATE_BUSINESS, now, license.InstanceID, license.Environment); !errors.Is(err, core.ErrDenied) {
		t.Fatalf("expired license must refuse business mutation: %v", err)
	}
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	for _, tt := range []struct {
		name        string
		instance    string
		login       bool
		permissions []string
		origin      string
		csrf        bool
		want        int
	}{
		{"expired history export remains available", license.InstanceID, true, []string{"filing.read"}, config.PublicOrigin, true, http.StatusOK},
		{"license denial prevents export execution", "different-instance", true, []string{"filing.read"}, config.PublicOrigin, true, http.StatusForbidden},
		{"history entitlement still requires login", license.InstanceID, false, []string{"filing.read"}, config.PublicOrigin, true, http.StatusUnauthorized},
		{"history entitlement still requires read permission", license.InstanceID, true, nil, config.PublicOrigin, true, http.StatusForbidden},
		{"history entitlement still requires trusted origin", license.InstanceID, true, []string{"filing.read"}, "https://other.example", true, http.StatusForbidden},
		{"history entitlement still requires csrf token", license.InstanceID, true, []string{"filing.read"}, config.PublicOrigin, false, http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repository := &filingExportLicenseRepository{value: filing.Filing{ID: 1, TenantID: config.TenantID, CustomerID: 7, AccountID: "subject-a", PublicID: "filing-public", FilingNo: "FIL-history", FormVersion: filing.FormVersion, Status: filing.StatusDraft, Version: 1}}
			renderer := &filingExportLicenseRenderer{content: []byte("%PDF-1.7\nexisting filing fixture\n")}
			codec, err := NewAEADCodec(bytes.Repeat([]byte{1}, 32))
			if err != nil {
				t.Fatal(err)
			}
			service := filing.NewService(repository, filingProtector{codec: codec}, nil, reportNotificationRouteClock{now: now}, filingExportLicenseID{}).UseExportRenderer(renderer)
			var operations []core.Operation
			router := NewRouter(RouterDependencies{
				Config:  config,
				Account: reportRiskRouteAccountService(t, tt.permissions),
				Filings: service,
				LicenseCheck: func(_ context.Context, operation core.Operation) error {
					operations = append(operations, operation)
					return core.Evaluate(license, "customer_portal", operation, now, tt.instance, license.Environment)
				},
			})
			request := httptest.NewRequest(http.MethodPost, config.PathPrefix+"/api/v1/filings/filing-public/exports", strings.NewReader(`{"expected_version":1}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", tt.origin)
			if tt.csrf {
				request.Header.Set("X-CSRF-Token", "1")
			}
			if tt.login {
				request.AddCookie(&http.Cookie{Name: config.SessionCookieName, Value: "opaque-session"})
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tt.want, response.Body.String())
			}
			if len(operations) != 1 || operations[0] != core.EXPORT_HISTORY {
				t.Fatalf("export route checked wrong license operation: %v", operations)
			}
			if tt.want != http.StatusOK {
				if repository.transactions != 0 || renderer.calls != 0 || len(repository.actions) != 0 {
					t.Fatalf("rejected request reached export service: transactions=%d renders=%d actions=%d", repository.transactions, renderer.calls, len(repository.actions))
				}
				if tt.instance != license.InstanceID && !strings.Contains(response.Body.String(), "COMMERCIAL_LICENSE_DENIED") {
					t.Fatalf("license rejection did not retain commercial error: %s", response.Body.String())
				}
				return
			}
			if repository.transactions != 1 || renderer.calls != 1 || len(repository.actions) != 1 || repository.actions[0].Action != "EXPORT_PDF" {
				t.Fatalf("history export did not render and audit: transactions=%d renders=%d actions=%v", repository.transactions, renderer.calls, repository.actions)
			}
			if !bytes.Equal(response.Body.Bytes(), renderer.content) || response.Header().Get("Content-Type") != "application/pdf" {
				t.Fatalf("unexpected export response: content-type=%s body=%s", response.Header().Get("Content-Type"), response.Body.String())
			}
			digest := sha256.Sum256(renderer.content)
			if response.Header().Get("X-Content-SHA256") != hex.EncodeToString(digest[:]) || repository.value.Status != filing.StatusDraft || repository.value.Version != 1 {
				t.Fatal("history export changed business state or lost content evidence")
			}
		})
	}
}

func filingExportExpiredLicense(t *testing.T, now time.Time) core.License {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	value := core.License{ProtocolVersion: core.ProtocolVersion, Issuer: core.Issuer, ID: "test-history", Version: 1, CustomerID: "test-customer", ProductID: core.Product, Environment: "production", InstanceID: "test-instance", IssuedAt: now.Add(-48 * time.Hour).Unix(), NotBefore: now.Add(-48 * time.Hour).Unix(), Applications: []core.Application{{Code: "customer_portal", NotBefore: now.Add(-24 * time.Hour).Unix(), ExpiresAt: now.Add(-time.Hour).Unix(), Kind: "FULL"}}}
	token, err := core.Sign(value, "test-export-key", private)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := core.Verify(token, map[string]ed25519.PublicKey{"test-export-key": public})
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

type filingExportLicenseID struct{}

func (filingExportLicenseID) NewID() string { return "filing-export-test-id" }

type filingExportLicenseRenderer struct {
	content []byte
	calls   int
}

func (r *filingExportLicenseRenderer) RenderRows(context.Context, []string) ([]byte, error) {
	r.calls++
	return r.content, nil
}

type filingExportLicenseRepository struct {
	filing.Repository
	value        filing.Filing
	transactions int
	actions      []filing.Action
}

func (r *filingExportLicenseRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	r.transactions++
	return fn(ctx)
}
func (r *filingExportLicenseRepository) FindOwnedForUpdate(_ context.Context, actor filing.Actor, id string) (*filing.Filing, error) {
	if actor.TenantID != r.value.TenantID || actor.CustomerID != r.value.CustomerID || actor.AccountID != r.value.AccountID || id != r.value.PublicID {
		return nil, filing.ErrNotFound
	}
	return &r.value, nil
}
func (*filingExportLicenseRepository) ListSections(context.Context, string, uint64) ([]filing.Section, error) {
	return nil, nil
}
func (*filingExportLicenseRepository) ListMatrices(context.Context, string, uint64) ([]filing.MatrixSelection, error) {
	return nil, nil
}
func (*filingExportLicenseRepository) ListMaterials(context.Context, string, uint64) ([]filing.Material, error) {
	return nil, nil
}
func (*filingExportLicenseRepository) LatestSubmission(context.Context, string, uint64) (*filing.SubmissionSnapshot, error) {
	return nil, filing.ErrNotFound
}
func (r *filingExportLicenseRepository) CreateAction(_ context.Context, value *filing.Action) error {
	r.actions = append(r.actions, *value)
	return nil
}
