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
	"strconv"
	"strings"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	signedContractCountScope            = "contract.opportunity_signed_count.read"
	maxSignedContractCountBatch         = 1000
	maxSignedContractCountResponseBytes = 256 << 10
)

// SignedContractCounter 只暴露商机维度的累计已签约合同数量，不把合同明细或状态模型带入 CRM。
type SignedContractCounter interface {
	CountSignedContracts(context.Context, []uint64) (map[uint64]uint64, error)
}

type SignedContractCounterOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	HTTPClient                                        *http.Client
	Now                                               func() time.Time
	NonceReader                                       io.Reader
}

type HTTPSignedContractCounter struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

type signedContractCountItem struct {
	OpportunityID       string `json:"opportunity_id"`
	SignedContractCount uint64 `json:"signed_contract_count"`
}

type signedContractCountEnvelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      *struct {
		Items []signedContractCountItem `json:"items"`
	} `json:"data,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}

func NewHTTPSignedContractCounter(ctx context.Context, options SignedContractCounterOptions) (*HTTPSignedContractCounter, error) {
	for name, value := range map[string]string{
		"endpoint": options.Endpoint, "token URL": options.TokenURL,
		"client ID": options.ClientID, "client secret": options.ClientSecret,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("signed contract count %s is required", name)
		}
	}
	if options.Scope != signedContractCountScope {
		return nil, errors.New("signed contract count machine scope is invalid")
	}
	if !validContractHTTPURL(options.Endpoint) || !validContractHTTPURL(options.TokenURL) {
		return nil, errors.New("signed contract count endpoint is invalid")
	}
	transport := http.DefaultTransport
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL,
		Scopes: []string{signedContractCountScope}, AuthStyle: oauth2.AuthStyleInHeader,
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &HTTPSignedContractCounter{
		endpoint: options.Endpoint,
		client: &http.Client{
			Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport},
			Timeout:   5 * time.Second,
		},
		now: now, nonceReader: nonceReader,
	}, nil
}

func (c *HTTPSignedContractCounter) CountSignedContracts(ctx context.Context, opportunityIDs []uint64) (map[uint64]uint64, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("signed contract count client is unavailable")
	}
	if len(opportunityIDs) == 0 {
		return map[uint64]uint64{}, nil
	}
	if len(opportunityIDs) > maxSignedContractCountBatch {
		return nil, errors.New("signed contract count batch is too large")
	}
	requested := make(map[uint64]struct{}, len(opportunityIDs))
	encodedIDs := make([]string, 0, len(opportunityIDs))
	for _, id := range opportunityIDs {
		if id == 0 {
			return nil, errors.New("signed contract count contains an invalid opportunity ID")
		}
		if _, duplicate := requested[id]; duplicate {
			return nil, errors.New("signed contract count contains a duplicate opportunity ID")
		}
		requested[id] = struct{}{}
		encodedIDs = append(encodedIDs, strconv.FormatUint(id, 10))
	}
	body, err := json.Marshal(struct {
		OpportunityIDs []string `json:"opportunity_ids"`
	}{OpportunityIDs: encodedIDs})
	if err != nil {
		return nil, errors.New("encode signed contract count request failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create signed contract count request failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Integration-Timestamp", c.now().UTC().Format(time.RFC3339Nano))
	if requestID := strings.TrimSpace(requestctx.ID(ctx)); requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(c.nonceReader, nonce); err != nil {
		return nil, errors.New("generate signed contract count request nonce failed")
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := c.client.Do(request)
	if err != nil {
		return nil, safeSignedContractCountTransportError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxSignedContractCountResponseBytes+1))
	if err != nil || len(raw) > maxSignedContractCountResponseBytes {
		return nil, errors.New("signed contract count endpoint returned an invalid response")
	}
	var envelope signedContractCountEnvelope
	if decodeSignedContractCountJSON(raw, &envelope) != nil || !validContractRequestID(envelope.RequestID) {
		return nil, errors.New("signed contract count endpoint returned an invalid response")
	}
	if response.StatusCode != http.StatusOK || envelope.Code != "OK" || envelope.Data == nil || len(envelope.Details) != 0 {
		return nil, fmt.Errorf("signed contract count endpoint returned HTTP %d", response.StatusCode)
	}
	if len(envelope.Data.Items) != len(requested) {
		return nil, errors.New("signed contract count endpoint returned an incomplete response")
	}
	counts := make(map[uint64]uint64, len(requested))
	for _, item := range envelope.Data.Items {
		id, parseErr := strconv.ParseUint(item.OpportunityID, 10, 64)
		if parseErr != nil || id == 0 || strconv.FormatUint(id, 10) != item.OpportunityID {
			return nil, errors.New("signed contract count endpoint returned an invalid opportunity ID")
		}
		if _, expected := requested[id]; !expected {
			return nil, errors.New("signed contract count endpoint returned an unknown opportunity ID")
		}
		if _, duplicate := counts[id]; duplicate {
			return nil, errors.New("signed contract count endpoint returned a duplicate opportunity ID")
		}
		counts[id] = item.SignedContractCount
	}
	return counts, nil
}

func decodeSignedContractCountJSON(raw []byte, target any) error {
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

func safeSignedContractCountTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("signed contract count request timed out")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("signed contract count request timed out")
	}
	return errors.New("signed contract count transport failed")
}
