package portalinvite

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPortalProvisionerUsesPlatformClientCredentialsAndReplayHeaders(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	transport := portalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://identity.example/oauth2/token":
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "crm-portal" || secret != "secret" {
				t.Fatalf("invalid client_secret_basic credentials: %q %q %v", clientID, secret, ok)
			}
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != "portal.identity_mapping.provision" || request.Form.Get("audience") != "" {
				t.Fatalf("unexpected token form: %v", request.Form)
			}
			return portalHTTPResponse(http.StatusOK, `{"access_token":"signed","token_type":"Bearer","expires_in":60}`), nil
		case "https://portal.example/customer-portal/internal/accounts/provision":
			if request.Header.Get("Authorization") != "Bearer signed" || request.Header.Get("X-Integration-Timestamp") != now.Format(time.RFC3339Nano) {
				t.Fatalf("integration headers: %v", request.Header)
			}
			nonce, err := base64.RawURLEncoding.DecodeString(request.Header.Get("X-Integration-Nonce"))
			if err != nil || len(nonce) != 32 {
				t.Fatalf("invalid nonce: %q %v", request.Header.Get("X-Integration-Nonce"), err)
			}
			return portalHTTPResponse(http.StatusCreated, `{"code":"OK","message":"success","request_id":"portal-request-1","data":{"portal_account_id":"P-1","account_no":"A-1"}}`), nil
		default:
			t.Fatalf("unexpected URL: %s", request.URL)
			return nil, nil
		}
	})
	provisioner, err := NewHTTPPortalProvisioner(context.Background(), PortalProvisionerOptions{
		Endpoint: "https://portal.example/customer-portal/internal/accounts/provision", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "crm-portal", ClientSecret: "secret", Scope: "portal.identity_mapping.provision",
		HTTPClient: &http.Client{Transport: transport}, Now: func() time.Time { return now }, NonceReader: strings.NewReader(strings.Repeat("n", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provisioner.ProvisionMapping(context.Background(), ContactIdentity{TenantID: "tenant-a", CustomerID: 1, ContactID: 2}, ProvisionedIdentity{AccountNo: "A-1", PlatformUserID: "sub-1"})
	if err != nil || result.PortalAccountID != "P-1" {
		t.Fatalf("ProvisionMapping() = %#v, %v", result, err)
	}
}

func TestPortalProvisionerUsesStableCompensationIdempotencyKey(t *testing.T) {
	transport := portalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://identity.example/oauth2/token":
			return portalHTTPResponse(http.StatusOK, `{"access_token":"signed","token_type":"Bearer","expires_in":60}`), nil
		case "https://portal.example/customer-portal/internal/accounts/provision":
			if request.Header.Get("Idempotency-Key") != "PCABC123" {
				t.Fatalf("idempotency key=%q", request.Header.Get("Idempotency-Key"))
			}
			return portalHTTPResponse(http.StatusCreated, `{"code":"OK","message":"success","request_id":"portal-request-2","data":{"portal_account_id":"P-1","account_no":"A-1"}}`), nil
		default:
			t.Fatalf("unexpected URL: %s", request.URL)
			return nil, nil
		}
	})
	provisioner, err := NewHTTPPortalProvisioner(context.Background(), PortalProvisionerOptions{
		Endpoint: "https://portal.example/customer-portal/internal/accounts/provision", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "crm-portal", ClientSecret: "secret", Scope: "portal.identity_mapping.provision",
		HTTPClient: &http.Client{Transport: transport}, NonceReader: strings.NewReader(strings.Repeat("n", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provisioner.ProvisionMappingIdempotent(context.Background(), ContactIdentity{TenantID: "tenant-a", CustomerID: 1, ContactID: 2}, ProvisionedIdentity{AccountNo: "A-1", PlatformUserID: "sub-1"}, "PCABC123")
	if err != nil {
		t.Fatal(err)
	}
}

func TestPortalProvisionerRejectsInvalidEndpointTLSAndResponseContracts(t *testing.T) {
	base := PortalProvisionerOptions{
		Endpoint: "https://portal.example/customer-portal/internal/accounts/provision", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "crm-portal", ClientSecret: "secret", Scope: "portal.identity_mapping.provision",
	}
	invalidEndpoint := base
	invalidEndpoint.Endpoint = "https://portal.example/provision?tenant=other"
	if _, err := NewHTTPPortalProvisioner(context.Background(), invalidEndpoint); err == nil {
		t.Fatal("query-bearing Portal endpoint was accepted")
	}
	requireMTLS := base
	requireMTLS.TLS.RequireMTLS = true
	if _, err := NewHTTPPortalProvisioner(context.Background(), requireMTLS); err == nil {
		t.Fatal("required mTLS without client identity was accepted")
	}

	for name, response := range map[string]struct {
		status int
		body   string
	}{
		"generic 2xx":      {http.StatusOK, `{"code":"OK","message":"success","request_id":"request-1","data":{"portal_account_id":"P-1","account_no":"A-1"}}`},
		"business failure": {http.StatusCreated, `{"code":"FAILED","message":"failed","request_id":"request-1","data":{"portal_account_id":"P-1","account_no":"A-1"}}`},
		"missing request":  {http.StatusCreated, `{"code":"OK","message":"success","request_id":"","data":{"portal_account_id":"P-1","account_no":"A-1"}}`},
		"unknown field":    {http.StatusCreated, `{"code":"OK","message":"success","request_id":"request-1","data":{"portal_account_id":"P-1","account_no":"A-1","tenant_id":"other"}}`},
		"trailing object":  {http.StatusCreated, `{"code":"OK","message":"success","request_id":"request-1","data":{"portal_account_id":"P-1","account_no":"A-1"}} {}`},
		"wrong account":    {http.StatusCreated, `{"code":"OK","message":"success","request_id":"request-1","data":{"portal_account_id":"P-1","account_no":"OTHER"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			transport := portalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() == base.TokenURL {
					return portalHTTPResponse(http.StatusOK, `{"access_token":"signed","token_type":"Bearer","expires_in":60}`), nil
				}
				return portalHTTPResponse(response.status, response.body), nil
			})
			options := base
			options.HTTPClient = &http.Client{Transport: transport}
			options.NonceReader = strings.NewReader(strings.Repeat("n", 32))
			client, err := NewHTTPPortalProvisioner(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.ProvisionMapping(context.Background(), ContactIdentity{TenantID: "tenant-a", CustomerID: 1, ContactID: 2}, ProvisionedIdentity{AccountNo: "A-1", PlatformUserID: "sub-1"}); err == nil {
				t.Fatal("invalid Portal response was accepted")
			}
		})
	}
}

func TestPortalProvisionerRejectsOversizedResponseAndInvalidIdentity(t *testing.T) {
	transport := portalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "oauth2") {
			return portalHTTPResponse(http.StatusOK, `{"access_token":"signed","token_type":"Bearer","expires_in":60}`), nil
		}
		return portalHTTPResponse(http.StatusCreated, strings.Repeat("x", maxIntegrationResponseBytes+1)), nil
	})
	client, err := NewHTTPPortalProvisioner(context.Background(), PortalProvisionerOptions{
		Endpoint: "https://portal.example/customer-portal/internal/accounts/provision", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "crm-portal", ClientSecret: "secret", Scope: "portal.identity_mapping.provision",
		HTTPClient: &http.Client{Transport: transport}, NonceReader: strings.NewReader(strings.Repeat("n", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ProvisionMapping(context.Background(), ContactIdentity{TenantID: "tenant-a", CustomerID: 1, ContactID: 2}, ProvisionedIdentity{AccountNo: "A-1", PlatformUserID: "sub-1"}); err == nil {
		t.Fatal("oversized response was accepted")
	}
	if _, err = client.ProvisionMapping(context.Background(), ContactIdentity{TenantID: "tenant-a", CustomerID: 0, ContactID: 2}, ProvisionedIdentity{AccountNo: "A-1", PlatformUserID: "sub-1"}); err == nil {
		t.Fatal("invalid mapping identity was accepted")
	}
}

type portalRoundTripFunc func(*http.Request) (*http.Response, error)

func (f portalRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func portalHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{URL: &url.URL{}}}
}
