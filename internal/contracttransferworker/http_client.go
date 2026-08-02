package contracttransferworker

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

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type signedCommand struct {
	EventID, TenantID, OpportunityNo, ContractRef, ExpectedAmount, SourceRequestID string
	OpportunityID, EventVersion, CustomerID                                        uint64
	OccurredAt                                                                     time.Time
}

type deliveryResult struct {
	IntakeID, Status, RequestID string
}

type permanentDeliveryError struct{ summary string }

func (e permanentDeliveryError) Error() string { return e.summary }

type contractClient struct {
	client *http.Client
	url    string
	now    func() time.Time
	nonce  func() (string, error)
}

func newContractClient(cfg Config) (*contractClient, error) {
	transport, err := integrationhttp.NewTransport(cfg.TLS, 2*time.Second)
	if err != nil {
		return nil, err
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	cc := clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, TokenURL: cfg.TokenURL, Scopes: []string{cfg.Scope}, AuthStyle: oauth2.AuthStyleInHeader}
	client := cc.Client(ctx)
	client.Timeout = 7 * time.Second
	return &contractClient{client: client, url: cfg.IntakeURL, now: time.Now, nonce: integrationNonce}, nil
}

func (c *contractClient) deliver(ctx context.Context, command signedCommand) (deliveryResult, error) {
	body, err := json.Marshal(struct {
		EventID         string    `json:"event_id"`
		TenantID        string    `json:"tenant_id"`
		OpportunityID   uint64    `json:"opportunity_id"`
		EventVersion    uint64    `json:"event_version"`
		OpportunityNo   string    `json:"opportunity_no"`
		CustomerID      uint64    `json:"customer_id"`
		ContractRef     string    `json:"contract_ref"`
		ExpectedAmount  string    `json:"expected_amount"`
		OccurredAt      time.Time `json:"occurred_at"`
		SourceRequestID string    `json:"source_request_id,omitempty"`
	}{command.EventID, command.TenantID, command.OpportunityID, command.EventVersion, command.OpportunityNo, command.CustomerID, command.ContractRef, command.ExpectedAmount, command.OccurredAt, command.SourceRequestID})
	if err != nil {
		return deliveryResult{}, err
	}
	nonce, err := c.nonce()
	if err != nil {
		return deliveryResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return deliveryResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", command.EventID)
	request.Header.Set("X-Integration-Timestamp", c.now().UTC().Format(time.RFC3339Nano))
	request.Header.Set("X-Integration-Nonce", nonce)
	response, err := c.client.Do(request)
	if err != nil {
		return deliveryResult{}, safeTransportError(err)
	}
	defer response.Body.Close()
	requestID := safeHeader(response.Header.Get("X-Request-ID"))
	if response.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		summary := fmt.Sprintf("contract intake status=%d", response.StatusCode)
		if requestID != "" {
			summary += " request_id=" + requestID
		}
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
			return deliveryResult{}, permanentDeliveryError{summary: summary}
		}
		return deliveryResult{}, errors.New(summary)
	}
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Data      struct {
			IntakeID string `json:"intake_id"`
			EventID  string `json:"event_id"`
			Status   string `json:"status"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err = decoder.Decode(&envelope); err != nil || envelope.Code != "OK" || envelope.Data.IntakeID == "" || envelope.Data.EventID != command.EventID || envelope.Data.Status != "ACCEPTED" {
		return deliveryResult{}, permanentDeliveryError{summary: "contract intake returned an invalid acceptance envelope"}
	}
	return deliveryResult{IntakeID: envelope.Data.IntakeID, Status: envelope.Data.Status, RequestID: requestID}, nil
}

func integrationNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("contract intake timed out")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("contract intake timed out")
	}
	return errors.New("contract intake transport failed")
}

func safeHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if !(char == '-' || char == '_' || char == '.' || char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z') {
			return ""
		}
	}
	return value
}
