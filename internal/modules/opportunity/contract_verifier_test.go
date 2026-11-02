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

func TestHTTPContractVerifierAcceptsCustomerMatchedContractWithoutOpportunity(t *testing.T) {
	// 合同可以先归属 CRM 客户、后关联商机；摘要返回 crm_opportunity_id=0 时
	// 仍应视为“属于该客户”，不能把正常签约核验错误判为无效响应。
	response := `{"code":"OK","message":"ok","request_id":"contract-request","data":{"contract_id":"01J00000000000000000000000","contract_number":"HT-1","crm_customer_id":7,"crm_opportunity_id":0,"linked_at":"2026-08-01T01:02:03Z"}}`
	verifier, closeServer := newContractVerifierForResponse(t, http.StatusOK, response)
	defer closeServer()
	belongs, err := verifier.BelongsToCustomer(context.Background(), "HT-1", 7)
	if err != nil || !belongs {
		t.Fatalf("BelongsToCustomer()=%v,%v", belongs, err)
	}
}

// TestHTTPContractVerifierParsesLinkEvidenceFields 固定“合同摘要新增证据字段可解析”：
// 合同子系统（BIZ-linked_at 方案 A）在成功响应里新增 linked_at_source /
// linked_at_precision / first_linked_at。本仓解码结构对这些字段零容忍
// （DisallowUnknownFields），因此字段白名单必须与合同子系统同批次放开；
// 放开后严格解码仍必须成功，且新增字段不得影响归属判定。
func TestHTTPContractVerifierParsesLinkEvidenceFields(t *testing.T) {
	response := `{"code":"OK","message":"ok","request_id":"contract-request","data":{"contract_id":"01J00000000000000000000000","contract_number":"HT-1","crm_customer_id":7,"crm_opportunity_id":9,"linked_at":"2026-08-01T01:02:03Z","linked_at_source":"AUDIT_LOG","linked_at_precision":"exact","first_linked_at":"2026-07-01T01:02:03Z"}}`
	verifier, closeServer := newContractVerifierForResponse(t, http.StatusOK, response)
	defer closeServer()
	belongs, err := verifier.BelongsToCustomer(context.Background(), "HT-1", 7)
	if err != nil || !belongs {
		t.Fatalf("BelongsToCustomer()=%v,%v 新证据字段必须可解析且不影响归属判定", belongs, err)
	}
	// 同一份响应必须能被严格解码成包含证据字段的结构；字段缺失或拼错时
	// DisallowUnknownFields 会在这里报错，而不是等生产调用才发现。
	var envelope contractSummaryEnvelope
	raw := []byte(response)
	if err := decodeContractSummaryJSON(raw, &envelope); err != nil || envelope.Data == nil {
		t.Fatalf("严格解码失败: %v data=%+v", err, envelope.Data)
	}
	data := envelope.Data
	if data.LinkedAtSource != "AUDIT_LOG" || data.LinkedAtPrecision != "exact" ||
		!data.FirstLinkedAt.Equal(time.Date(2026, 7, 1, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("证据字段解析错误: %+v", data)
	}
}

// TestHTTPContractVerifierDoesNotRejectApproximateLinkedAt 固定“approx 不拒绝核验”：
// 历史合同没有关联证据时合同侧回退 updated_at 并标注 APPROX_CONTRACT_UPDATED/approx。
// 精度只是告知调用方该时间不能用于判断业务先后顺序，客户一致时核验必须照常通过。
func TestHTTPContractVerifierDoesNotRejectApproximateLinkedAt(t *testing.T) {
	response := `{"code":"OK","message":"ok","request_id":"contract-request","data":{"contract_id":"01J00000000000000000000000","contract_number":"HT-1","crm_customer_id":7,"crm_opportunity_id":0,"linked_at":"2026-08-01T01:02:03Z","linked_at_source":"APPROX_CONTRACT_UPDATED","linked_at_precision":"approx","first_linked_at":"2026-08-01T01:02:03Z"}}`
	verifier, closeServer := newContractVerifierForResponse(t, http.StatusOK, response)
	defer closeServer()
	belongs, err := verifier.BelongsToCustomer(context.Background(), "HT-1", 7)
	if err != nil || !belongs {
		t.Fatalf("BelongsToCustomer()=%v,%v approx 精度不得导致核验失败", belongs, err)
	}
	// 精度不是客户归属：客户不一致时 approx 也必须判为“不属于该客户”。
	mismatch := strings.Replace(response, `"crm_customer_id":7`, `"crm_customer_id":8`, 1)
	verifier, closeServer = newContractVerifierForResponse(t, http.StatusOK, mismatch)
	defer closeServer()
	if belongs, err := verifier.BelongsToCustomer(context.Background(), "HT-1", 7); err != nil || belongs {
		t.Fatalf("BelongsToCustomer()=%v,%v 客户不一致必须判为不属于", belongs, err)
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
