package opportunity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPQBStatusReaderUsesExactScopeNonceAndOpportunityID(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	var resourceRequest *http.Request
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if request.FormValue("scope") != qbStatusReadScope {
				t.Errorf("scope = %q", request.FormValue("scope"))
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"access_token":"machine-token","token_type":"Bearer","expires_in":300}`))
		case "/status":
			resourceRequest = request.Clone(request.Context())
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"ok","request_id":"req-1","data":{"opportunity_id":7,"latest":{"type":"报价","source_id":"BJ-1","status":"报价已通过","source_amount":"100.00","changed_at":"2026-08-01T08:00:00Z"}}}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	reader, err := NewHTTPQBStatusReader(context.Background(), QBStatusReaderOptions{
		Endpoint: server.URL + "/status", TokenURL: server.URL + "/token", ClientID: "crm", ClientSecret: "secret", Scope: qbStatusReadScope,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now }, NonceReader: strings.NewReader(strings.Repeat("n", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := reader.LatestByOpportunity(context.Background(), 7)
	if err != nil || latest == nil || latest.SourceID != "BJ-1" {
		t.Fatalf("latest=%#v err=%v", latest, err)
	}
	if resourceRequest == nil || resourceRequest.URL.Query().Get("opportunityId") != "7" || resourceRequest.Header.Get("Authorization") != "Bearer machine-token" || resourceRequest.Header.Get("X-Integration-Timestamp") != now.Format(time.RFC3339Nano) {
		t.Fatalf("resource request = %#v", resourceRequest)
	}
	if _, err := base64.RawURLEncoding.DecodeString(resourceRequest.Header.Get("X-Integration-Nonce")); err != nil {
		t.Fatalf("nonce is invalid: %v", err)
	}
	if resourceRequest.Header.Get("X-Tenant-ID") != "" {
		t.Fatal("reader must not accept or forward a browser-controlled tenant header")
	}
}

func TestHTTPQBStatusReaderRejectsUnknownOrContradictoryResponse(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":     `{"code":"OK","message":"ok","request_id":"r","data":{"opportunity_id":7,"latest":null,"tenant_id":"other"}}`,
		"wrong opportunity": `{"code":"OK","message":"ok","request_id":"r","data":{"opportunity_id":8,"latest":null}}`,
		"trailing":          `{"code":"OK","message":"ok","request_id":"r","data":{"opportunity_id":7,"latest":null}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := qbTestServer(body)
			defer server.Close()
			reader, err := NewHTTPQBStatusReader(context.Background(), QBStatusReaderOptions{Endpoint: server.URL + "/status", TokenURL: server.URL + "/token", ClientID: "crm", ClientSecret: "secret", Scope: qbStatusReadScope, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = reader.LatestByOpportunity(context.Background(), 7); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestExternalLaunchSignerReturnsFixedURLAndVerifiableShortLivedContext(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	key := []byte(strings.Repeat("k", 32))
	signer, err := NewExternalLaunchSigner(ExternalLaunchSignerOptions{
		QuotationURL: "https://qb.example/quotation", BidURL: "https://qb.example/bid", Key: key, TTL: 2 * time.Minute,
		Now: func() time.Time { return now }, NonceReader: strings.NewReader(strings.Repeat("n", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &Opportunity{CustomerID: 3, RequirementSummary: "仅传需求摘要", Status: StatusFollowing}
	model.ID = 7
	result, err := signer.Sign("tenant-a", model, "报价")
	if err != nil || result.LaunchURL != "https://qb.example/quotation" || !result.ExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	parts := strings.Split(result.Context, ".")
	if len(parts) != 3 || parts[0] != externalLaunchTokenPrefix {
		t.Fatalf("context = %q", result.Context)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims externalLaunchClaims
	if err = json.Unmarshal(payload, &claims); err != nil || claims.OpportunityID != 7 || claims.CustomerID != 3 || claims.TenantID != "tenant-a" || claims.Purpose != "QUOTATION_CREATE" || claims.RequirementSummary != "仅传需求摘要" {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
	if strings.Contains(result.Context, "tenant-a") || strings.Contains(result.Context, "仅传需求摘要") {
		t.Fatal("context payload must use URL-safe encoding instead of raw query material")
	}
}

func TestExternalLaunchSignerRejectsCallerURLAndLongTTL(t *testing.T) {
	if _, err := NewExternalLaunchSigner(ExternalLaunchSignerOptions{QuotationURL: "http://qb.example/quotation", BidURL: "https://qb.example/bid", Key: []byte(strings.Repeat("k", 32)), TTL: time.Minute}); err == nil {
		t.Fatal("clear-text launch URL accepted")
	}
	if _, err := NewExternalLaunchSigner(ExternalLaunchSignerOptions{QuotationURL: "https://qb.example/quotation?next=evil", BidURL: "https://qb.example/bid", Key: []byte(strings.Repeat("k", 32)), TTL: time.Minute}); err == nil {
		t.Fatal("query-bearing launch URL accepted")
	}
	if _, err := NewExternalLaunchSigner(ExternalLaunchSignerOptions{QuotationURL: "https://qb.example/quotation", BidURL: "https://qb.example/bid", Key: []byte(strings.Repeat("k", 32)), TTL: 6 * time.Minute}); err == nil {
		t.Fatal("overlong launch context accepted")
	}
}

func TestHTTPQBStatusReaderProductionConfigurationRequiresHTTPS(t *testing.T) {
	if _, err := NewHTTPQBStatusReader(context.Background(), QBStatusReaderOptions{
		Endpoint: "http://qb.example/status", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "crm", ClientSecret: "secret", Scope: qbStatusReadScope,
	}); err == nil {
		t.Fatal("clear-text quotation/bid endpoint was accepted")
	}
}

func qbTestServer(body string) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":300}`))
			return
		}
		if request.URL.Path == "/status" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(body))
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
}

func TestValidLaunchPublicURLDoesNotAcceptInjectedDestination(t *testing.T) {
	for _, raw := range []string{"https://qb.example/launch?url=" + url.QueryEscape("https://evil.example"), "//evil.example", "javascript:alert(1)"} {
		if validLaunchPublicURL(raw) {
			t.Errorf("invalid launch URL %q accepted", raw)
		}
	}
}
