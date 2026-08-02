package portalreportworker

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
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type projectServiceClient struct {
	client   *http.Client
	endpoint string
	now      func() time.Time
	nonce    func() (string, error)
}

func newProjectServiceClient(cfg ProjectServiceConfig) (*projectServiceClient, error) {
	transport, err := integrationhttp.NewTransport(cfg.TLS, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return newProjectServiceClientWithTransport(cfg, transport), nil
}

func newProjectServiceClientWithTransport(cfg ProjectServiceConfig, transport http.RoundTripper) *projectServiceClient {
	credentials := clientcredentials.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, TokenURL: cfg.TokenURL,
		Scopes: strings.Fields(cfg.Scope), AuthStyle: oauth2.AuthStyleInHeader,
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	client := credentials.Client(ctx)
	client.Timeout = 5 * time.Second
	return &projectServiceClient{client: client, endpoint: cfg.RequestURL, now: func() time.Time { return time.Now().UTC() }, nonce: randomNonce}
}

func (c *projectServiceClient) Submit(ctx context.Context, event report.Outbox) (string, error) {
	nonce, err := c.nonce()
	if err != nil {
		return "", errors.New("generate integration nonce failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(event.Payload))
	if err != nil {
		return "", errors.New("build project-service request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", event.EventID)
	req.Header.Set("X-Integration-Timestamp", c.now().UTC().Format(time.RFC3339Nano))
	req.Header.Set("X-Integration-Nonce", nonce)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", safeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		requestID := sanitizeHeader(resp.Header.Get("X-Request-ID"))
		if requestID != "" {
			return "", fmt.Errorf("project-service response status=%d request_id=%s", resp.StatusCode, requestID)
		}
		return "", fmt.Errorf("project-service response status=%d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			DownstreamRequestID string `json:"downstream_request_id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return "", errors.New("invalid project-service response")
	}
	downstreamID := strings.TrimSpace(envelope.Data.DownstreamRequestID)
	if downstreamID == "" || len(downstreamID) > 128 {
		return "", errors.New("project-service response is missing request identity")
	}
	return downstreamID, nil
}

func randomNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func safeTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("project-service request timed out")
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("project-service request timed out")
	}
	return errors.New("project-service transport failed")
}

func sanitizeHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return ""
		}
	}
	return value
}
