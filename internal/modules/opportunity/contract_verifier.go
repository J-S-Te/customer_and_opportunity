package opportunity

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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	contractSummaryScope            = "contract.summary.read"
	maxContractSummaryResponseBytes = 64 << 10
)

type ContractVerifierOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	HTTPClient                                        *http.Client
	Now                                               func() time.Time
	NonceReader                                       io.Reader
}

// HTTPContractVerifier 是 CRM 调用合同子系统“客户归属合同摘要”接口的防腐层，
// 只接收验证所需最小投影，不把合同内部模型引入商机模块。
type HTTPContractVerifier struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

type contractSummary struct {
	ContractID       string    `json:"contract_id"`
	ContractNumber   string    `json:"contract_number"`
	CRMCustomerID    uint64    `json:"crm_customer_id"`
	CRMOpportunityID uint64    `json:"crm_opportunity_id"`
	LinkedAt         time.Time `json:"linked_at"`
}

type contractSummaryEnvelope struct {
	Code      string           `json:"code"`
	Message   string           `json:"message"`
	RequestID string           `json:"request_id"`
	Data      *contractSummary `json:"data,omitempty"`
	Details   json.RawMessage  `json:"details,omitempty"`
}

func NewHTTPContractVerifier(ctx context.Context, options ContractVerifierOptions) (*HTTPContractVerifier, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("contract summary %s is required", name)
		}
	}
	if options.Scope != contractSummaryScope {
		return nil, errors.New("contract summary machine scope is invalid")
	}
	if !validContractHTTPURL(options.Endpoint) {
		return nil, errors.New("contract summary endpoint is invalid")
	}
	if !validContractHTTPURL(options.TokenURL) {
		return nil, errors.New("contract summary token URL is invalid")
	}
	transport := http.DefaultTransport
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL,
		Scopes: []string{contractSummaryScope}, AuthStyle: oauth2.AuthStyleInHeader,
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &HTTPContractVerifier{
		endpoint: options.Endpoint,
		client:   &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport}, Timeout: 10 * time.Second},
		now:      now, nonceReader: nonceReader,
	}, nil
}

func (v *HTTPContractVerifier) BelongsToCustomer(ctx context.Context, contractRef string, customerID uint64) (bool, error) {
	contractRef = strings.TrimSpace(contractRef)
	if v == nil || v.client == nil || customerID == 0 || !validContractNumber(contractRef) {
		return false, errors.New("contract summary request is invalid")
	}
	query := url.Values{"contract_ref": {contractRef}, "crm_customer_id": {fmt.Sprint(customerID)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return false, errors.New("create contract summary request failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Integration-Timestamp", v.now().UTC().Format(time.RFC3339Nano))
	if requestID := strings.TrimSpace(requestctx.ID(ctx)); requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(v.nonceReader, nonce); err != nil {
		return false, errors.New("generate contract summary request nonce failed")
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := v.client.Do(request)
	if err != nil {
		return false, safeContractTransportError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxContractSummaryResponseBytes+1))
	if err != nil || len(raw) > maxContractSummaryResponseBytes {
		return false, errors.New("contract summary endpoint returned an invalid response")
	}
	var envelope contractSummaryEnvelope
	if decodeContractSummaryJSON(raw, &envelope) != nil || !validContractRequestID(envelope.RequestID) {
		return false, errors.New("contract summary endpoint returned an invalid response")
	}
	if response.StatusCode == http.StatusNotFound && envelope.Code == "CON_NOT_FOUND" && envelope.Data == nil {
		return false, nil
	}
	if response.StatusCode != http.StatusOK || envelope.Code != "OK" || envelope.Data == nil || len(envelope.Details) != 0 {
		return false, fmt.Errorf("contract summary endpoint returned HTTP %d", response.StatusCode)
	}
	summary := envelope.Data
	if summary.CRMCustomerID != customerID {
		return false, nil
	}
	if !validCanonicalULID(summary.ContractID) || !validContractNumber(summary.ContractNumber) ||
		(summary.ContractID != contractRef && summary.ContractNumber != contractRef) ||
		summary.CRMOpportunityID == 0 || summary.LinkedAt.IsZero() {
		return false, errors.New("contract summary endpoint returned an invalid response")
	}
	return true, nil
}

func validContractHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func decodeContractSummaryJSON(raw []byte, target any) error {
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

func validCanonicalULID(value string) bool {
	if len(value) != 26 || value[0] < '0' || value[0] > '7' {
		return false
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for i := range value {
		if !strings.ContainsRune(alphabet, rune(value[i])) {
			return false
		}
	}
	return true
}

func validContractNumber(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || len([]rune(value)) > 64 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validContractRequestID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}

func safeContractTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("contract summary request timed out")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("contract summary request timed out")
	}
	return errors.New("contract summary transport failed")
}
