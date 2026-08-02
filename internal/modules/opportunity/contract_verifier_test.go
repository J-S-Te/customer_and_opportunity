package opportunity

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

func TestHTTPContractVerifierUsesDedicatedTokenAndStrictResourceRequest(t *testing.T) {
	var mutex sync.Mutex
	var nonces, timestamps []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "crm-contract" || secret != "secret" {
				t.Errorf("token basic auth=(%q,%q,%v)", clientID, secret, ok)
			}
			if err := request.ParseForm(); err != nil || request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != contractSummaryScope {
				t.Errorf("token form=%v err=%v", request.Form, err)
			}
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprint(response, `{"access_token":"token","token_type":"Bearer","expires_in":3600,"scope":"contract.summary.read"}`)
		case "/internal/contract-summary":
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("resource method/auth=%s/%q", request.Method, request.Header.Get("Authorization"))
			}
			if request.Header.Get("X-Tenant-ID") != "" || request.Header.Get("X-Request-ID") != "crm-request" {
				t.Errorf("unexpected tenant/request headers: %q/%q", request.Header.Get("X-Tenant-ID"), request.Header.Get("X-Request-ID"))
			}
			query := request.URL.Query()
			if len(query) != 2 || len(query["contract_ref"]) != 1 || query.Get("contract_ref") != "HT-2026-001" || len(query["crm_customer_id"]) != 1 || query.Get("crm_customer_id") != "7" {
				t.Errorf("query=%v", query)
			}
			mutex.Lock()
			nonces = append(nonces, request.Header.Get("X-Integration-Nonce"))
			timestamps = append(timestamps, request.Header.Get("X-Integration-Timestamp"))
			mutex.Unlock()
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprint(response, `{"code":"OK","message":"操作成功","request_id":"01JCONTRACTREQUEST000000001","data":{"contract_id":"01J00000000000000000000000","contract_number":"HT-2026-001","crm_customer_id":7,"crm_opportunity_id":9,"linked_at":"2026-08-01T01:02:03Z"}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	current := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	verifier, err := NewHTTPContractVerifier(context.Background(), ContractVerifierOptions{
		Endpoint: server.URL + "/internal/contract-summary", TokenURL: server.URL + "/token",
		ClientID: "crm-contract", ClientSecret: "secret", Scope: contractSummaryScope,
		Now:         func() time.Time { current = current.Add(time.Nanosecond); return current },
		NonceReader: bytes.NewReader(append(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)...)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestctx.WithID(context.Background(), "crm-request")
	for range 2 {
		belongs, verifyErr := verifier.BelongsToCustomer(ctx, " HT-2026-001 ", 7)
		if verifyErr != nil || !belongs {
			t.Fatalf("BelongsToCustomer()=%v,%v", belongs, verifyErr)
		}
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] || timestamps[0] == timestamps[1] {
		t.Fatalf("nonces=%v timestamps=%v", nonces, timestamps)
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(nonces[0]); err != nil || len(decoded) != 32 {
		t.Fatalf("nonce=%q decoded=%d err=%v", nonces[0], len(decoded), err)
	}
}

func TestHTTPContractVerifierNotFoundAndCustomerMismatchReturnFalse(t *testing.T) {
	tests := []struct {
		name, response string
		status         int
	}{
		{"not found", `{"code":"CON_NOT_FOUND","message":"not found","request_id":"contract-request"}`, http.StatusNotFound},
		{"customer mismatch", `{"code":"OK","message":"ok","request_id":"contract-request","data":{"contract_id":"01J00000000000000000000000","contract_number":"HT-1","crm_customer_id":8,"crm_opportunity_id":9,"linked_at":"2026-08-01T01:02:03Z"}}`, http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier, closeServer := newContractVerifierForResponse(t, test.status, test.response)
			defer closeServer()
			belongs, err := verifier.BelongsToCustomer(context.Background(), "HT-1", 7)
			if err != nil || belongs {
				t.Fatalf("BelongsToCustomer()=%v,%v", belongs, err)
			}
		})
	}
}

func TestHTTPContractVerifierRejectsMalformedAndOversizedResponses(t *testing.T) {
	valid := `{"code":"OK","message":"ok","request_id":"contract-request","data":{"contract_id":"01J00000000000000000000000","contract_number":"HT-1","crm_customer_id":7,"crm_opportunity_id":9,"linked_at":"2026-08-01T01:02:03Z"}}`
	tests := []struct{ name, response string }{
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"unexpected":true}`},
		{"trailing JSON", valid + `{}`},
		{"missing request id", strings.Replace(valid, `"request_id":"contract-request"`, `"request_id":""`, 1)},
		{"oversized", strings.Repeat("x", maxContractSummaryResponseBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier, closeServer := newContractVerifierForResponse(t, http.StatusOK, test.response)
			defer closeServer()
			if belongs, err := verifier.BelongsToCustomer(context.Background(), "HT-1", 7); err == nil || belongs {
				t.Fatalf("BelongsToCustomer()=%v,%v", belongs, err)
			}
		})
	}
}

func TestNewHTTPContractVerifierRejectsOverScopedAndQueriedURLs(t *testing.T) {
	base := ContractVerifierOptions{Endpoint: "https://contract.example/internal/contract-summary", TokenURL: "https://identity.example/token", ClientID: "id", ClientSecret: "secret", Scope: contractSummaryScope}
	overScoped := base
	overScoped.Scope += " contract.write"
	if _, err := NewHTTPContractVerifier(context.Background(), overScoped); err == nil {
		t.Fatal("expected over-scoped configuration rejection")
	}
	queried := base
	queried.Endpoint += "?tenant=hidden"
	if _, err := NewHTTPContractVerifier(context.Background(), queried); err == nil {
		t.Fatal("expected queried endpoint rejection")
	}
}

func newContractVerifierForResponse(t *testing.T, status int, body string) (*HTTPContractVerifier, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprint(response, `{"access_token":"token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		response.WriteHeader(status)
		fmt.Fprint(response, body)
	}))
	verifier, err := NewHTTPContractVerifier(context.Background(), ContractVerifierOptions{
		Endpoint: server.URL + "/summary", TokenURL: server.URL + "/token", ClientID: "id", ClientSecret: "secret", Scope: contractSummaryScope,
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return verifier, server.Close
}

func TestContractSummaryQueryEncodingIsStrict(t *testing.T) {
	values := url.Values{"contract_ref": {"HT A&B"}, "crm_customer_id": {"7"}}
	if encoded := values.Encode(); !strings.Contains(encoded, "contract_ref=HT+A%26B") {
		t.Fatalf("encoded=%q", encoded)
	}
}
