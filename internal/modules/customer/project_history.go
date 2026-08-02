package customer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	projectHistoryScope            = "portal.project_history.read"
	maxProjectHistoryResponseBytes = 256 << 10
	maxProjectHistoryPage          = 1_000_000
)

// ProjectHistoryItem is the CRM anti-corruption projection of Portal-owned
// project snapshot data. Portal account, manager identity and team fields are
// intentionally not part of this contract.
type ProjectHistoryItem struct {
	ProjectID         string     `json:"project_id"`
	ProjectName       string     `json:"project_name"`
	ContractNo        string     `json:"contract_no"`
	Status            string     `json:"status"`
	ProgressPct       uint8      `json:"progress_pct"`
	CurrentStage      string     `json:"current_stage"`
	ExpectedEndDate   *time.Time `json:"expected_end_date,omitempty"`
	Delayed           bool       `json:"delayed"`
	SourceUpdatedAt   time.Time  `json:"source_updated_at"`
	SyncedAt          time.Time  `json:"synced_at"`
	SyncLastSuccessAt *time.Time `json:"sync_last_success_at"`
	Stale             bool       `json:"stale"`
	StalenessSeconds  *int64     `json:"staleness_seconds"`
}

type ProjectHistoryPage struct {
	Items    []ProjectHistoryItem `json:"items"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
	Total    int64                `json:"total"`
}

type ProjectHistoryReader interface {
	ListCustomerProjects(context.Context, string, uint64, int, int) (ProjectHistoryPage, error)
}

type ProjectHistoryReaderOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	HTTPClient                                        *http.Client
	Now                                               func() time.Time
	NonceReader                                       io.Reader
}

// HTTPProjectHistoryReader calls the Portal-owned, application-JWT-protected
// projection endpoint with a dedicated client-credentials identity.
type HTTPProjectHistoryReader struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

func NewHTTPProjectHistoryReader(ctx context.Context, options ProjectHistoryReaderOptions) (*HTTPProjectHistoryReader, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("Portal project history %s is required", name)
		}
	}
	if strings.TrimSpace(options.Scope) != projectHistoryScope {
		return nil, errors.New("Portal project history machine scope is invalid")
	}
	endpoint, err := url.ParseRequestURI(strings.TrimRight(options.Endpoint, "/"))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("Portal project history endpoint is invalid")
	}
	tokenURL, err := url.ParseRequestURI(options.TokenURL)
	if err != nil || (tokenURL.Scheme != "http" && tokenURL.Scheme != "https") || tokenURL.Host == "" || tokenURL.User != nil || tokenURL.RawQuery != "" || tokenURL.Fragment != "" {
		return nil, errors.New("Portal project history token URL is invalid")
	}
	transport := http.DefaultTransport
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL,
		Scopes: []string{projectHistoryScope}, AuthStyle: oauth2.AuthStyleInHeader,
	}
	client := &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport}, Timeout: 10 * time.Second}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &HTTPProjectHistoryReader{endpoint: strings.TrimRight(options.Endpoint, "/"), client: client, now: now, nonceReader: nonceReader}, nil
}

func (r *HTTPProjectHistoryReader) ListCustomerProjects(ctx context.Context, tenantID string, customerID uint64, page, pageSize int) (ProjectHistoryPage, error) {
	if r == nil || r.client == nil || strings.TrimSpace(tenantID) == "" || tenantID != strings.TrimSpace(tenantID) || customerID == 0 || page < 1 || page > maxProjectHistoryPage || pageSize < 1 || pageSize > 100 {
		return ProjectHistoryPage{}, errors.New("Portal project history request is invalid")
	}
	endpoint := r.endpoint + "/" + strconv.FormatUint(customerID, 10) + "/projects"
	query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return ProjectHistoryPage{}, errors.New("create Portal project history request failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Integration-Timestamp", r.now().UTC().Format(time.RFC3339Nano))
	if requestID := strings.TrimSpace(requestctx.ID(ctx)); requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(r.nonceReader, nonce); err != nil {
		return ProjectHistoryPage{}, errors.New("generate Portal project history request nonce failed")
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := r.client.Do(request)
	if err != nil {
		return ProjectHistoryPage{}, safeProjectHistoryTransportError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProjectHistoryResponseBytes+1))
	if err != nil || len(raw) > maxProjectHistoryResponseBytes {
		return ProjectHistoryPage{}, errors.New("Portal project history endpoint returned an invalid response")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ProjectHistoryPage{}, fmt.Errorf("Portal project history endpoint returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code      string             `json:"code"`
		Message   string             `json:"message"`
		RequestID string             `json:"request_id"`
		Data      ProjectHistoryPage `json:"data"`
	}
	if err = decodeProjectHistoryJSON(raw, &envelope); err != nil || envelope.Code != "OK" || !validProjectHistoryRequestID(envelope.RequestID) || !validProjectHistoryPage(envelope.Data, page, pageSize) {
		return ProjectHistoryPage{}, errors.New("Portal project history endpoint returned an invalid response")
	}
	return envelope.Data, nil
}

func validProjectHistoryRequestID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}

func decodeProjectHistoryJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("response contains trailing JSON")
	}
	return nil
}

func validProjectHistoryPage(page ProjectHistoryPage, expectedPage, expectedPageSize int) bool {
	if page.Items == nil || page.Page != expectedPage || page.PageSize != expectedPageSize || page.Total < int64(len(page.Items)) || len(page.Items) > page.PageSize {
		return false
	}
	// A non-empty page must start inside the declared total, while an empty
	// page may only be the first empty page or a page after the final item.
	// This rejects contradictory dependency envelopes such as page 2 with one
	// item and total 1 without assuming how the Portal repository counts rows.
	offset := int64(page.Page-1) * int64(page.PageSize)
	if len(page.Items) > 0 && (offset >= page.Total || offset+int64(len(page.Items)) > page.Total) {
		return false
	}
	seen := make(map[string]struct{}, len(page.Items))
	for _, item := range page.Items {
		if !boundedRequired(item.ProjectID, 64) || !boundedRequired(item.ProjectName, 200) || !boundedOptional(item.ContractNo, 64) || !boundedRequired(item.Status, 32) || item.ProgressPct > 100 || !boundedRequired(item.CurrentStage, 64) || item.SourceUpdatedAt.IsZero() || item.SyncedAt.IsZero() || !validProjectHistoryFreshness(item) {
			return false
		}
		if _, duplicate := seen[item.ProjectID]; duplicate {
			return false
		}
		seen[item.ProjectID] = struct{}{}
	}
	return true
}

func validProjectHistoryFreshness(item ProjectHistoryItem) bool {
	if item.SyncLastSuccessAt == nil {
		return item.Stale && item.StalenessSeconds == nil
	}
	// A page in the current synchronization run may have been persisted before
	// a later page fails. In that legitimate state the row's SyncedAt is newer
	// than the customer's last *complete* successful synchronization. The two
	// timestamps describe different facts, so imposing an ordering between them
	// would reject a valid stale projection supplied by Portal.
	return !item.SyncLastSuccessAt.IsZero() && item.StalenessSeconds != nil && *item.StalenessSeconds >= 0
}

func boundedRequired(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len([]rune(value)) <= maximum
}

func boundedOptional(value string, maximum int) bool {
	return value == "" || value == strings.TrimSpace(value) && len([]rune(value)) <= maximum
}

func safeProjectHistoryTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Portal project history request timed out")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("Portal project history request timed out")
	}
	return errors.New("Portal project history transport failed")
}
