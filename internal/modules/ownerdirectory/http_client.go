package ownerdirectory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	ownerDirectoryScope           = "owner_directory.read"
	maximumDirectoryResponseBytes = 256 << 10
)

type HTTPOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	TLS                                               integrationhttp.TLSOptions
	HTTPClient                                        *http.Client
}

type HTTPClient struct {
	endpoint string
	client   *http.Client
}

func NewHTTPClient(ctx context.Context, options HTTPOptions) (*HTTPClient, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("owner directory %s is required", name)
		}
	}
	if options.Scope != ownerDirectoryScope || !validURL(options.Endpoint) || !validURL(options.TokenURL) {
		return nil, errors.New("owner directory configuration is invalid")
	}
	if err := options.TLS.ValidateEndpoints(options.Endpoint, options.TokenURL); err != nil {
		return nil, fmt.Errorf("owner directory TLS: %w", err)
	}
	var transport http.RoundTripper
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	} else {
		var err error
		transport, err = integrationhttp.NewTransport(options.TLS, 3*time.Second)
		if err != nil {
			return nil, err
		}
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: rejectRedirect}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL, Scopes: []string{ownerDirectoryScope}, AuthStyle: oauth2.AuthStyleInHeader}
	// 获取令牌和访问目录共用经过 TLS 约束的传输层，并禁止重定向；凭据不能被 3xx 带到未配置主机。
	client := &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport}, Timeout: 10 * time.Second, CheckRedirect: rejectRedirect}
	return &HTTPClient{endpoint: options.Endpoint, client: client}, nil
}

func (client *HTTPClient) List(ctx context.Context, query Query) (Page, error) {
	endpoint, err := url.Parse(client.endpoint)
	if err != nil {
		return Page{}, ErrUnavailable
	}
	values := endpoint.Query()
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		values.Set("keyword", keyword)
	}
	if userID := strings.TrimSpace(query.UserID); userID != "" {
		values.Set("user_id", userID)
	}
	if query.Page > 0 {
		values.Set("page", strconv.Itoa(query.Page))
	}
	if query.PageSize > 0 {
		values.Set("page_size", strconv.Itoa(query.PageSize))
	}
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Page{}, ErrUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return Page{}, ErrUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumDirectoryResponseBytes+1))
	if err != nil || len(raw) > maximumDirectoryResponseBytes || response.StatusCode != http.StatusOK || !isJSON(response.Header.Get("Content-Type")) {
		return Page{}, ErrUnavailable
	}
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Data      Page   `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// 平台目录是授权决策输入，响应必须严格匹配合同且只能包含一个 JSON 值，
	// 避免字段漂移或尾随内容被静默忽略。
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&envelope); err != nil || envelope.Code != "OK" {
		return Page{}, ErrUnavailable
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return Page{}, ErrUnavailable
	}
	return envelope.Data, nil
}

func (client *HTTPClient) Validate(ctx context.Context, userID, organizationID string) error {
	page, err := client.List(ctx, Query{UserID: strings.TrimSpace(userID), Page: 1, PageSize: 1})
	if err != nil {
		return normalizeError(err)
	}
	return validatePair(page, userID, organizationID)
}

// Resolve resolves at most one authoritative directory record for every requested platform subject.
// Exact lookups are bounded so a large opportunity team cannot create an unbounded request burst.
func (client *HTTPClient) Resolve(ctx context.Context, userIDs []string) (map[string]User, error) {
	unique := make([]string, 0, len(userIDs))
	seen := make(map[string]struct{}, len(userIDs))
	for _, raw := range userIDs {
		userID := strings.TrimSpace(raw)
		if userID == "" {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		unique = append(unique, userID)
	}
	resolved := make(map[string]User, len(unique))
	if len(unique) == 0 {
		return resolved, nil
	}

	type lookupResult struct {
		userID string
		user   *User
		err    error
	}
	lookupContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan lookupResult, len(unique))
	semaphore := make(chan struct{}, 8)
	var wait sync.WaitGroup
	for _, userID := range unique {
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-lookupContext.Done():
				results <- lookupResult{userID: userID, err: lookupContext.Err()}
				return
			}
			defer func() { <-semaphore }()
			page, err := client.List(lookupContext, Query{UserID: userID, Page: 1, PageSize: 1})
			if err != nil {
				results <- lookupResult{userID: userID, err: err}
				return
			}
			for index := range page.Items {
				if page.Items[index].ID == userID {
					user := page.Items[index]
					results <- lookupResult{userID: userID, user: &user}
					return
				}
			}
			results <- lookupResult{userID: userID}
		}()
	}
	go func() {
		wait.Wait()
		close(results)
	}()

	var lookupErr error
	for result := range results {
		if result.err != nil {
			lookupErr = result.err
			cancel()
			continue
		}
		if result.user != nil {
			resolved[result.userID] = *result.user
		}
	}
	if lookupErr != nil {
		return nil, ErrUnavailable
	}
	return resolved, nil
}

func rejectRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func isJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func validURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}
