package requestaudit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type DispatcherOptions struct {
	PlatformBaseURL, ClientID, ClientSecret, ApplicationCode, EnvironmentCode, WorkerID string
	PollInterval                                                                        time.Duration
	BatchSize                                                                           int
	HTTPClient                                                                          *http.Client
}

type Dispatcher struct {
	store                    *Store
	baseURL                  string
	clientID, clientSecret   string
	application, environment string
	workerID                 string
	pollInterval             time.Duration
	batchSize                int
	client                   *http.Client
	mu                       sync.Mutex
	token                    string
	tokenExpiresAt           time.Time
}

type platformHTTPError struct{ status int }

func (e platformHTTPError) Error() string {
	return fmt.Sprintf("platform audit returned status %d", e.status)
}

var errOutboxSourceMismatch = errors.New("audit outbox source does not match dispatcher configuration")
var errAuditConfigurationMismatch = errors.New("platform audit publisher binding does not match dispatcher configuration")

func NewDispatcher(store *Store, options DispatcherOptions) (*Dispatcher, error) {
	if store == nil {
		return nil, errors.New("request audit store is required")
	}
	for name, value := range map[string]string{
		"platform base URL": options.PlatformBaseURL, "audit client ID": options.ClientID,
		"audit client secret": options.ClientSecret, "application code": options.ApplicationCode,
		"environment code": options.EnvironmentCode, "worker ID": options.WorkerID,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s is required and must be trimmed", name)
		}
	}
	parsed, err := url.ParseRequestURI(options.PlatformBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("platform base URL must be an HTTP(S) origin")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = time.Second
	}
	if options.BatchSize <= 0 || options.BatchSize > 100 {
		options.BatchSize = 100
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Dispatcher{
		store: store, baseURL: strings.TrimRight(options.PlatformBaseURL, "/"), clientID: options.ClientID,
		clientSecret: options.ClientSecret, application: options.ApplicationCode, environment: options.EnvironmentCode,
		workerID: options.WorkerID, pollInterval: options.PollInterval, batchSize: options.BatchSize, client: &clientCopy,
	}, nil
}

func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	for {
		if err := d.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			// The durable row remains RETRY. The caller's structured logger records
			// process health; this package never logs payloads or credentials.
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ValidateConfiguration performs a read-only, authenticated preflight against
// the platform audit contract. Callers should report its error but must not use
// it to block business startup: the durable Outbox remains the fallback while
// the platform is temporarily unavailable.
func (d *Dispatcher) ValidateConfiguration(ctx context.Context) error {
	token, err := d.accessToken(ctx)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/api/v1/audit/ingest/validate", nil)
	if err != nil {
		return fmt.Errorf("create platform audit validation request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := d.client.Do(request)
	if err != nil {
		return fmt.Errorf("request platform audit validation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return platformHTTPError{status: response.StatusCode}
	}
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			ApplicationCode string `json:"application_code"`
			EnvironmentCode string `json:"environment_code"`
			ClientID        string `json:"client_id"`
			AuditIngest     bool   `json:"audit_ingest"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil || envelope.Code != "OK" {
		return errors.New("platform audit validation returned an invalid response envelope")
	}
	if envelope.Data.ApplicationCode != d.application || envelope.Data.EnvironmentCode != d.environment || envelope.Data.ClientID != d.clientID || !envelope.Data.AuditIngest {
		return errAuditConfigurationMismatch
	}
	return nil
}

func (d *Dispatcher) runOnce(ctx context.Context) error {
	now := time.Now().UTC()
	if err := d.store.RecoverInterrupted(ctx, now.Add(-5*time.Minute), now); err != nil {
		return err
	}
	values, err := d.store.Claim(ctx, d.workerID, d.batchSize, now, now.Add(30*time.Second))
	if err != nil || len(values) == 0 {
		return err
	}
	if err = d.deliver(ctx, values); err != nil {
		next := now.Add(retryDelay(values))
		retryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = d.store.Retry(retryContext, d.workerID, values, deliveryErrorCode(err), next, now)
		return err
	}
	return d.store.Delivered(ctx, d.workerID, values, now)
}

func (d *Dispatcher) deliver(ctx context.Context, values []Record) error {
	for _, value := range values {
		if value.ApplicationCode != d.application || value.EnvironmentCode != d.environment {
			return errOutboxSourceMismatch
		}
	}
	token, err := d.accessToken(ctx)
	if err != nil {
		return err
	}
	events := make([]map[string]any, 0, len(values))
	for _, value := range values {
		trace := sha256.Sum256([]byte(value.EventID))
		event := map[string]any{
			"event_id": value.EventID, "occurred_at": value.OccurredAt.UTC().Format(time.RFC3339Nano),
			"application_code": value.ApplicationCode, "environment_code": value.EnvironmentCode,
			"actor_type": value.ActorType, "action": value.Action, "resource_type": value.ResourceType,
			"resource_id": value.ResourceID,
			"request_id":  value.RequestID, "trace_id": hex.EncodeToString(trace[:16]), "correlation_id": value.RequestID,
			"result": value.Result, "risk_level": value.RiskLevel, "reason_code": value.ReasonCode,
			"summary": "Subsystem operation audit", "metadata": map[string]any{"method": value.Method, "path": value.Route, "status_code": value.HTTPStatus},
		}
		if value.ActorID != "" {
			event["actor_id"] = value.ActorID
		}
		if value.ActorName != "" {
			event["actor_name"] = value.ActorName
		}
		events = append(events, event)
	}
	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		return err
	}
	requestID, traceID, parentID, err := correlationIDs()
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/v1/audit/events/batch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	request.Header.Set("X-Correlation-ID", requestID)
	request.Header.Set("traceparent", "00-"+traceID+"-"+parentID+"-01")
	response, err := d.client.Do(request)
	if err != nil {
		return fmt.Errorf("send platform audit batch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return platformHTTPError{status: response.StatusCode}
	}
	var envelope struct {
		Code string `json:"code"`
		Data []struct {
			EventID string `json:"event_id"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil || envelope.Code != "OK" {
		return errors.New("platform audit batch returned an invalid response envelope")
	}
	expected := make(map[string]struct{}, len(values))
	for _, value := range values {
		expected[value.EventID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(envelope.Data))
	for _, receipt := range envelope.Data {
		if _, ok := expected[receipt.EventID]; !ok {
			return errors.New("platform audit batch returned an unknown receipt")
		}
		if _, duplicate := seen[receipt.EventID]; duplicate {
			return errors.New("platform audit batch returned a duplicate receipt")
		}
		if receipt.Status != "ACCEPTED" && receipt.Status != "DUPLICATE" {
			return errors.New("platform audit batch rejected an event")
		}
		seen[receipt.EventID] = struct{}{}
	}
	if len(seen) != len(expected) {
		return errors.New("platform audit batch omitted an event receipt")
	}
	return nil
}

func (d *Dispatcher) accessToken(ctx context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.token != "" && time.Until(d.tokenExpiresAt) > 30*time.Second {
		return d.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"audit.ingest"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.SetBasicAuth(d.clientID, d.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := d.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request platform audit token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return "", platformHTTPError{status: response.StatusCode}
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil {
		return "", err
	}
	if scopes := strings.Fields(token.Scope); token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") || token.ExpiresIn <= 0 || len(scopes) != 1 || scopes[0] != "audit.ingest" {
		return "", errors.New("platform token is missing the exact audit.ingest grant")
	}
	d.token, d.tokenExpiresAt = token.AccessToken, time.Now().Add(time.Duration(token.ExpiresIn)*time.Second)
	return d.token, nil
}

func correlationIDs() (string, string, string, error) {
	raw := make([]byte, 40)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	return hex.EncodeToString(raw[:16]), hex.EncodeToString(raw[16:32]), hex.EncodeToString(raw[32:]), nil
}

func retryDelay(values []Record) time.Duration {
	var attempts uint32
	for _, value := range values {
		if value.Attempts > attempts {
			attempts = value.Attempts
		}
	}
	delay := time.Second << min(attempts, 11)
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func deliveryErrorCode(err error) string {
	if errors.Is(err, errOutboxSourceMismatch) {
		return "PLATFORM_AUDIT_OUTBOX_SOURCE_MISMATCH"
	}
	if errors.Is(err, errAuditConfigurationMismatch) {
		return "PLATFORM_AUDIT_CONFIGURATION_MISMATCH"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "PLATFORM_AUDIT_TIMEOUT"
	}
	var response platformHTTPError
	if errors.As(err, &response) {
		switch response.status {
		case http.StatusUnauthorized:
			return "PLATFORM_AUDIT_UNAUTHORIZED"
		case http.StatusForbidden:
			return "PLATFORM_AUDIT_FORBIDDEN"
		case http.StatusUnprocessableEntity:
			return "PLATFORM_AUDIT_CLIENT_BINDING_REJECTED"
		case http.StatusTooManyRequests:
			return "PLATFORM_AUDIT_RATE_LIMITED"
		default:
			if response.status >= http.StatusInternalServerError {
				return "PLATFORM_AUDIT_SERVER_ERROR"
			}
		}
	}
	return "PLATFORM_AUDIT_DELIVERY_FAILED"
}

// DeliveryErrorCode provides a stable, credential-free diagnostic category for
// health endpoints and structured logs. It never returns remote response
// bodies, which could contain sensitive implementation details.
func DeliveryErrorCode(err error) string { return deliveryErrorCode(err) }
