package ownerdirectory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHTTPClientUsesExactScopeAndParsesPlatformEnvelope(t *testing.T) {
	var directoryQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			if clientID, clientSecret, ok := request.BasicAuth(); !ok || clientID != "crm-owner-reader" || clientSecret != "secret" {
				t.Fatalf("unexpected token authentication: id=%q ok=%v", clientID, ok)
			}
			if err := request.ParseForm(); err != nil || request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != ownerDirectoryScope {
				t.Fatalf("token form=%v err=%v", request.Form, err)
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 300})
		case "/api/v1/internal/owner-directory":
			if request.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
			}
			directoryQuery = request.URL.Query()
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"code": "OK", "message": "负责人目录查询成功", "request_id": "request-1",
				"data": Page{Items: []User{{ID: "oidc-sub-1", DisplayName: "张三", Organizations: []Organization{{ID: "org-1", Name: "华东区", IsPrimary: true}}}}, Page: 1, PageSize: 1, Total: 1},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewHTTPClient(context.Background(), HTTPOptions{
		Endpoint: server.URL + "/api/v1/internal/owner-directory", TokenURL: server.URL + "/oauth2/token",
		ClientID: "crm-owner-reader", ClientSecret: "secret", Scope: ownerDirectoryScope, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.List(context.Background(), Query{UserID: "oidc-sub-1", Page: 1, PageSize: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Organizations[0].ID != "org-1" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if directoryQuery.Get("user_id") != "oidc-sub-1" || directoryQuery.Get("page") != "1" || directoryQuery.Get("page_size") != "1" {
		t.Fatalf("directory query=%v", directoryQuery)
	}
	if err = client.Validate(context.Background(), "oidc-sub-1", "org-1"); err != nil {
		t.Fatalf("Validate() error=%v", err)
	}
}

func TestValidatePairRejectsMissingOrUnrelatedOrganization(t *testing.T) {
	page := Page{Items: []User{{ID: "user-1", Organizations: []Organization{{ID: "org-1"}}}}}
	for _, selection := range [][2]string{{"", "org-1"}, {"user-1", ""}, {"user-2", "org-1"}, {"user-1", "org-2"}} {
		if err := validatePair(page, selection[0], selection[1]); err != ErrSelectionInvalid {
			t.Fatalf("selection=%v err=%v", selection, err)
		}
	}
}

func TestResolveReturnsOnlyExactActiveDirectoryUsers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 300})
		case "/api/v1/internal/owner-directory":
			userID := request.URL.Query().Get("user_id")
			items := []User{}
			if userID == "user-active" {
				items = append(items, User{ID: userID, DisplayName: "真实人员"})
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"code": "OK", "message": "success", "request_id": "request-resolve",
				"data": Page{Items: items, Page: 1, PageSize: 1, Total: int64(len(items))},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewHTTPClient(context.Background(), HTTPOptions{
		Endpoint: server.URL + "/api/v1/internal/owner-directory", TokenURL: server.URL + "/oauth2/token",
		ClientID: "crm-owner-reader", ClientSecret: "secret", Scope: ownerDirectoryScope, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	users, err := client.Resolve(context.Background(), []string{" user-active ", "user-missing", "user-active"})
	if err != nil || len(users) != 1 || users["user-active"].DisplayName != "真实人员" {
		t.Fatalf("users=%#v err=%v", users, err)
	}
}
