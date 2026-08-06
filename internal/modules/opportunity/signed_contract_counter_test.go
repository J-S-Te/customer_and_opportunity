package opportunity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

func TestHTTPSignedContractCounterBatchesAndValidatesResponse(t *testing.T) {
	fixedNow := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "crm-count" || secret != "secret" {
				t.Errorf("unexpected token auth: %q %q %v", clientID, secret, ok)
			}
			if err := request.ParseForm(); err != nil || request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != signedContractCountScope {
				t.Errorf("unexpected token form: %#v err=%v", request.Form, err)
			}
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"access_token":"signed","token_type":"Bearer","expires_in":3600,"scope":"contract.opportunity_signed_count.read"}`)
		case "/contract_management/internal/opportunity-contract-counts/query":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer signed" || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("unexpected count request: method=%s headers=%v", request.Method, request.Header)
			}
			if request.Header.Get("X-Request-ID") != "crm-request" {
				t.Errorf("unexpected request ID: %q", request.Header.Get("X-Request-ID"))
			}
			if request.Header.Get("X-Integration-Timestamp") != fixedNow.Format(time.RFC3339Nano) || request.Header.Get("X-Integration-Nonce") == "" {
				t.Errorf("missing integration replay headers: %v", request.Header)
			}
			var body struct {
				OpportunityIDs []string `json:"opportunity_ids"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || strings.Join(body.OpportunityIDs, ",") != "7,9" {
				t.Errorf("body=%#v err=%v", body, err)
			}
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"code":"OK","message":"success","request_id":"contract-request","data":{"items":[{"opportunity_id":"9","signed_contract_count":0},{"opportunity_id":"7","signed_contract_count":2}]}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	counter, err := NewHTTPSignedContractCounter(context.Background(), SignedContractCounterOptions{
		Endpoint: server.URL + "/contract_management/internal/opportunity-contract-counts/query",
		TokenURL: server.URL + "/oauth2/token", ClientID: "crm-count", ClientSecret: "secret",
		Scope: signedContractCountScope, HTTPClient: server.Client(), Now: func() time.Time { return fixedNow },
		NonceReader: bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestctx.WithID(context.Background(), "crm-request")
	counts, err := counter.CountSignedContracts(ctx, []uint64{7, 9})
	if err != nil || counts[7] != 2 || counts[9] != 0 || len(counts) != 2 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
}

func TestHTTPSignedContractCounterRejectsTokenFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth2/token" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(writer, `{"error":"invalid_client"}`)
			return
		}
		t.Fatal("resource endpoint must not be called after token failure")
	}))
	defer server.Close()
	counter := newSignedContractCountTestClient(t, server)
	if _, err := counter.CountSignedContracts(context.Background(), []uint64{7}); err == nil {
		t.Fatal("token failure returned success")
	}
}

func TestHTTPSignedContractCounterRejectsMalformedOrIncompleteResponses(t *testing.T) {
	tests := map[string]string{
		"missing item":     `{"code":"OK","message":"success","request_id":"r","data":{"items":[]}}`,
		"duplicate item":   `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"opportunity_id":"7","signed_contract_count":1},{"opportunity_id":"7","signed_contract_count":1}]}}`,
		"unknown field":    `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"opportunity_id":"7","signed_contract_count":1,"contracts":[]}]}}`,
		"non canonical id": `{"code":"OK","message":"success","request_id":"r","data":{"items":[{"opportunity_id":"07","signed_contract_count":1}]}}`,
		"invalid envelope": `{"code":"OK","message":"success","data":{"items":[{"opportunity_id":"7","signed_contract_count":1}]}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			server := signedContractCountTestServer(t, raw, http.StatusOK)
			defer server.Close()
			counter := newSignedContractCountTestClient(t, server)
			ids := []uint64{7}
			if name == "duplicate item" {
				ids = []uint64{7, 9}
			}
			if _, err := counter.CountSignedContracts(context.Background(), ids); err == nil {
				t.Fatal("malformed response was accepted")
			}
		})
	}
}

func TestHTTPSignedContractCounterRejectsInvalidRequestsAndScope(t *testing.T) {
	if _, err := NewHTTPSignedContractCounter(context.Background(), SignedContractCounterOptions{
		Endpoint: "https://contract.example/query", TokenURL: "https://identity.example/token",
		ClientID: "client", ClientSecret: "secret", Scope: "contract.read",
	}); err == nil {
		t.Fatal("over-scoped client was accepted")
	}
	server := signedContractCountTestServer(t, `{"code":"OK","message":"success","request_id":"r","data":{"items":[]}}`, http.StatusOK)
	defer server.Close()
	counter := newSignedContractCountTestClient(t, server)
	if _, err := counter.CountSignedContracts(context.Background(), []uint64{0}); err == nil {
		t.Fatal("zero opportunity ID was accepted")
	}
	if _, err := counter.CountSignedContracts(context.Background(), []uint64{7, 7}); err == nil {
		t.Fatal("duplicate opportunity ID was accepted")
	}
	tooMany := make([]uint64, maxSignedContractCountBatch+1)
	for index := range tooMany {
		tooMany[index] = uint64(index + 1)
	}
	if _, err := counter.CountSignedContracts(context.Background(), tooMany); err == nil {
		t.Fatal("oversized batch was accepted")
	}
}

func TestHTTPSignedContractCounterHonorsCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth2/token" {
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"access_token":"signed","token_type":"Bearer","expires_in":3600}`)
			return
		}
		select {
		case <-request.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	counter := newSignedContractCountTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := counter.CountSignedContracts(ctx, []uint64{7}); err == nil {
		t.Fatal("cancelled request returned success")
	}
}

func signedContractCountTestServer(t *testing.T, raw string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth2/token" {
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"access_token":"signed","token_type":"Bearer","expires_in":3600}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		fmt.Fprint(writer, raw)
	}))
}

func newSignedContractCountTestClient(t *testing.T, server *httptest.Server) *HTTPSignedContractCounter {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	counter, err := NewHTTPSignedContractCounter(context.Background(), SignedContractCounterOptions{
		Endpoint: parsed.String() + "/query", TokenURL: parsed.String() + "/oauth2/token",
		ClientID: "client", ClientSecret: "secret", Scope: signedContractCountScope,
		HTTPClient: server.Client(), NonceReader: bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return counter
}
