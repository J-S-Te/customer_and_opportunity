package portalprojectworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPSourceUsesPlatformOAuthContractAndCustomerScope(t *testing.T) {
	const secret = "do-not-leak-client-secret"
	const accessToken = "do-not-leak-token"
	now := time.Date(2026, 8, 1, 1, 2, 3, 456000000, time.UTC)
	var mu sync.Mutex
	var tokenCalls int
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/oauth2/token":
			tokenCalls++
			id, gotSecret, ok := request.BasicAuth()
			if !ok || id != "portal-project" || gotSecret != secret {
				t.Errorf("unexpected client authentication")
			}
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != requiredProjectScope {
				t.Errorf("unexpected token form: %v", request.Form)
			}
			if _, exists := request.Form["audience"]; exists {
				t.Errorf("platform contract does not accept audience: %v", request.Form)
			}
			return jsonResponse(http.StatusOK, map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 300, "scope": requiredProjectScope}), nil
		case "/api/v1/projects/snapshots":
			if request.Header.Get("Authorization") != "Bearer "+accessToken || request.Header.Get("X-Tenant-ID") != "tenant-a" || request.Header.Get("X-Integration-Timestamp") != now.Format(time.RFC3339Nano) || request.Header.Get("X-Integration-Nonce") == "" {
				t.Errorf("missing machine headers: %v", request.Header)
			}
			if request.URL.Query().Get("customerId") != "77" || request.URL.Query().Get("cursor") != "cursor-1" || request.URL.Query().Get("limit") != "100" {
				t.Errorf("unexpected query: %v", request.URL.Query())
			}
			return jsonResponse(http.StatusOK, validEnvelope("project-77", "cursor-2", false)), nil
		default:
			return jsonResponse(http.StatusNotFound, map[string]any{"private": "not found"}), nil
		}
	})
	source := testHTTPSource(secret, transport)
	source.now = func() time.Time { return now }
	page, err := source.changed(context.Background(), "tenant-a", 77, "cursor-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Bundles) != 1 || page.Bundles[0].ProjectID != "project-77" || page.NextCursor != "cursor-2" {
		t.Fatalf("unexpected page: %#v", page)
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenCalls != 1 {
		t.Fatalf("token calls=%d", tokenCalls)
	}
}

func TestHTTPSourceRejectsNonAdvancingCursorAndBadFiveMilestones(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"cursor": func(value map[string]any) { value["has_more"], value["next_cursor"] = true, "same" },
		"milestones": func(value map[string]any) {
			items := value["items"].([]map[string]any)
			items[0]["milestones"] = items[0]["milestones"].([]map[string]any)[:4]
		},
	} {
		t.Run(name, func(t *testing.T) {
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/oauth2/token" {
					return jsonResponse(http.StatusOK, map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 300}), nil
				}
				envelope := validEnvelope("project-a", "next", false)
				data := envelope["data"].(map[string]any)
				mutate(data)
				return jsonResponse(http.StatusOK, envelope), nil
			})
			source := testHTTPSource("secret", transport)
			_, err := source.changed(context.Background(), "tenant-a", 1, "same")
			if err == nil {
				t.Fatal("expected invalid source response")
			}
		})
	}
}

func TestHTTPSourceErrorsDoNotLeakCredentialsOrBody(t *testing.T) {
	const secret = "private-project-client-secret"
	const body = "upstream-private-diagnostics"
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/oauth2/token" {
			return jsonResponse(http.StatusOK, map[string]any{"access_token": "private-access-token", "token_type": "Bearer", "expires_in": 300}), nil
		}
		return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	source := testHTTPSource(secret, transport)
	_, err := source.changed(context.Background(), "tenant-a", 1, "")
	if err == nil {
		t.Fatal("expected dependency error")
	}
	for _, forbidden := range []string{secret, body, "private-access-token", "customerId=1"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

func TestSafeIntegrationTransportErrorDoesNotExposeURL(t *testing.T) {
	err := safeIntegrationTransportError(&url.Error{Op: "Get", URL: "https://example.test?secret=value", Err: errors.New("private detail")})
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func testHTTPSource(secret string, transport http.RoundTripper) *httpSource {
	cfg := Config{TokenURL: "https://identity.example/oauth2/token", ClientID: "portal-project", ClientSecret: secret, Scope: requiredProjectScope, SnapshotsURL: "https://project.example/api/v1/projects/snapshots", TokenTimeout: time.Second, RequestTimeout: time.Second, PageSize: 100}
	return newHTTPSourceWithTransport(context.Background(), cfg, transport)
}

func jsonResponse(status int, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func validEnvelope(projectID, nextCursor string, hasMore bool) map[string]any {
	milestones := make([]map[string]any, 5)
	for i := range milestones {
		milestones[i] = map[string]any{"stage_code": "stage", "stage_name": "Stage", "status": "PENDING", "sort_no": i}
	}
	return map[string]any{"code": "OK", "data": map[string]any{"items": []map[string]any{{
		"project_id": projectID, "project_name": "Project", "status": "IN_PROGRESS", "progress_pct": 50,
		"current_stage": "stage", "source_updated_at": "2026-08-01T01:00:00Z", "raw_version": "v1",
		"milestones": milestones, "activities": []map[string]any{}, "team": []map[string]any{},
	}}, "next_cursor": nextCursor, "has_more": hasMore}}
}
