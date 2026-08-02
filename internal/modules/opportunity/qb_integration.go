package opportunity

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	qbStatusReadScope         = "opportunity.status.read"
	maxQBStatusResponseBytes  = 64 << 10
	maxExternalLaunchTTL      = 5 * time.Minute
	externalLaunchTokenPrefix = "v1"
)

type QBStatusReader interface {
	LatestByOpportunity(context.Context, uint64) (*ExternalStatusSnapshot, error)
}

type QBStatusReaderOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	TLS                                               integrationhttp.TLSOptions
	HTTPClient                                        *http.Client
	Now                                               func() time.Time
	NonceReader                                       io.Reader
}

type HTTPQBStatusReader struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

func NewHTTPQBStatusReader(ctx context.Context, options QBStatusReaderOptions) (*HTTPQBStatusReader, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("quotation/bid status %s is required", name)
		}
	}
	if options.Scope != qbStatusReadScope {
		return nil, errors.New("quotation/bid status machine scope is invalid")
	}
	if !validQBHTTPURL(options.Endpoint) || !validQBHTTPURL(options.TokenURL) {
		return nil, errors.New("quotation/bid status endpoint is invalid")
	}
	if err := options.TLS.ValidateEndpoints(options.Endpoint, options.TokenURL); err != nil {
		return nil, fmt.Errorf("quotation/bid TLS configuration: %w", err)
	}
	transport := http.RoundTripper(http.DefaultTransport)
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	} else {
		built, err := integrationhttp.NewTransport(options.TLS, 2*time.Second)
		if err != nil {
			return nil, fmt.Errorf("quotation/bid HTTP transport: %w", err)
		}
		transport = built
	}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: rejectQBRedirect})
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL,
		Scopes: []string{qbStatusReadScope}, AuthStyle: oauth2.AuthStyleInHeader,
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &HTTPQBStatusReader{
		endpoint: options.Endpoint,
		client: &http.Client{Transport: &oauth2.Transport{
			Source: credentials.TokenSource(tokenContext), Base: transport,
		}, Timeout: 10 * time.Second, CheckRedirect: rejectQBRedirect},
		now: now, nonceReader: nonceReader,
	}, nil
}

func (reader *HTTPQBStatusReader) LatestByOpportunity(ctx context.Context, opportunityID uint64) (*ExternalStatusSnapshot, error) {
	if reader == nil || reader.client == nil || opportunityID == 0 {
		return nil, errors.New("quotation/bid status request is invalid")
	}
	query := url.Values{"opportunityId": {strconv.FormatUint(opportunityID, 10)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, reader.endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, errors.New("create quotation/bid status request failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Integration-Timestamp", reader.now().UTC().Format(time.RFC3339Nano))
	if requestID := strings.TrimSpace(requestctx.ID(ctx)); requestID != "" {
		if !validQBRequestID(requestID) {
			return nil, errors.New("quotation/bid status request identity is invalid")
		}
		request.Header.Set("X-Request-ID", requestID)
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(reader.nonceReader, nonce); err != nil {
		return nil, errors.New("generate quotation/bid status request nonce failed")
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := reader.client.Do(request)
	if err != nil {
		return nil, safeQBTransportError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxQBStatusResponseBytes+1))
	if err != nil || len(raw) > maxQBStatusResponseBytes {
		return nil, errors.New("quotation/bid status endpoint returned an invalid response")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quotation/bid status endpoint returned HTTP %d", response.StatusCode)
	}
	if !qbJSONContentType(response.Header.Get("Content-Type")) {
		return nil, errors.New("quotation/bid status endpoint returned an invalid response")
	}
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Data      struct {
			OpportunityID uint64                  `json:"opportunity_id"`
			Latest        *ExternalStatusSnapshot `json:"latest"`
		} `json:"data"`
	}
	if decodeStrictQBJSON(raw, &envelope) != nil || envelope.Code != "OK" || !validQBRequestID(envelope.RequestID) || envelope.Data.OpportunityID != opportunityID {
		return nil, errors.New("quotation/bid status endpoint returned an invalid response")
	}
	if envelope.Data.Latest != nil && !validExternalSnapshot(*envelope.Data.Latest) {
		return nil, errors.New("quotation/bid status endpoint returned an invalid response")
	}
	return envelope.Data.Latest, nil
}

func validExternalSnapshot(snapshot ExternalStatusSnapshot) bool {
	if snapshot.Type != "报价" && snapshot.Type != "投标" || !statusMatchesExternalType(snapshot.Type, snapshot.Status) || strings.TrimSpace(snapshot.SourceID) != snapshot.SourceID || snapshot.SourceID == "" || len([]byte(snapshot.SourceID)) > 64 || snapshot.ChangedAt.IsZero() {
		return false
	}
	if snapshot.SourceAmount != nil {
		input := ExternalStatusRequest{Type: snapshot.Type, SourceID: snapshot.SourceID, Status: snapshot.Status, SourceAmount: snapshot.SourceAmount, ChangedAt: snapshot.ChangedAt}
		if _, _, err := normalizeExternalStatus(input); err != nil {
			return false
		}
	}
	return snapshot.ContractRef == nil && snapshot.LostReason == nil
}

func decodeStrictQBJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("quotation/bid response contains trailing JSON")
	}
	return nil
}

func validQBHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func rejectQBRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func qbJSONContentType(value string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return strings.EqualFold(mediaType, "application/json")
}

func validQBRequestID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}

func safeQBTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("quotation/bid status request timed out")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("quotation/bid status request timed out")
	}
	return errors.New("quotation/bid status transport failed")
}

type ExternalLaunchSignerOptions struct {
	QuotationURL string
	BidURL       string
	Key          []byte
	TTL          time.Duration
	Now          func() time.Time
	NonceReader  io.Reader
}

type ExternalLaunchSigner struct {
	quotationURL string
	bidURL       string
	key          []byte
	ttl          time.Duration
	now          func() time.Time
	nonceReader  io.Reader
}

type ExternalLaunchResponse struct {
	Type      string    `json:"type"`
	LaunchURL string    `json:"launch_url"`
	Context   string    `json:"context"`
	ExpiresAt time.Time `json:"expires_at"`
}

type externalLaunchClaims struct {
	Version            uint8  `json:"v"`
	Purpose            string `json:"purpose"`
	TenantID           string `json:"tenant_id"`
	OpportunityID      uint64 `json:"opportunity_id"`
	CustomerID         uint64 `json:"customer_id"`
	RequirementSummary string `json:"requirement_summary"`
	ExpiresAt          int64  `json:"exp"`
	Nonce              string `json:"nonce"`
}

func NewExternalLaunchSigner(options ExternalLaunchSignerOptions) (*ExternalLaunchSigner, error) {
	if !validLaunchPublicURL(options.QuotationURL) || !validLaunchPublicURL(options.BidURL) {
		return nil, errors.New("quotation/bid public launch URL is invalid")
	}
	if len(options.Key) < 32 {
		return nil, errors.New("quotation/bid launch signing key must contain at least 32 bytes")
	}
	if options.TTL <= 0 || options.TTL > maxExternalLaunchTTL {
		return nil, errors.New("quotation/bid launch context TTL is invalid")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &ExternalLaunchSigner{quotationURL: options.QuotationURL, bidURL: options.BidURL, key: append([]byte(nil), options.Key...), ttl: options.TTL, now: now, nonceReader: nonceReader}, nil
}

func (signer *ExternalLaunchSigner) Sign(tenantID string, model *Opportunity, launchType string) (ExternalLaunchResponse, error) {
	if signer == nil || model == nil || tenantID == "" || tenantID != strings.TrimSpace(tenantID) || model.ID == 0 || model.CustomerID == 0 || model.Status == StatusVoid {
		return ExternalLaunchResponse{}, errors.New("quotation/bid launch context is invalid")
	}
	launchURL, purpose := signer.quotationURL, "QUOTATION_CREATE"
	if launchType == "投标" {
		launchURL, purpose = signer.bidURL, "BID_CREATE"
	} else if launchType != "报价" {
		return ExternalLaunchResponse{}, errors.New("quotation/bid launch type is invalid")
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(signer.nonceReader, nonce); err != nil {
		return ExternalLaunchResponse{}, errors.New("generate quotation/bid launch nonce failed")
	}
	now := signer.now().UTC()
	expiresAt := now.Add(signer.ttl)
	claims := externalLaunchClaims{
		Version: 1, Purpose: purpose, TenantID: tenantID, OpportunityID: model.ID,
		CustomerID: model.CustomerID, RequirementSummary: model.RequirementSummary,
		ExpiresAt: expiresAt.Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return ExternalLaunchResponse{}, errors.New("encode quotation/bid launch context failed")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, signer.key)
	_, _ = mac.Write([]byte(externalLaunchTokenPrefix + "." + encoded))
	token := externalLaunchTokenPrefix + "." + encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return ExternalLaunchResponse{Type: launchType, LaunchURL: launchURL, Context: token, ExpiresAt: expiresAt}, nil
}

func validLaunchPublicURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}
