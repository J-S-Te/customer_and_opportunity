package crmauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// brokerVerifier records a successful CRM Keycloak login with the base
// platform.  The access token is used only by the CRM server: it carries the
// platform session (sid) and subject which the receiving endpoint binds to the
// supplied identity_id.  It is never exposed to the browser.
type brokerVerifier interface {
	Verify(context.Context, verifiedClaims) error
}

type platformBrokerVerifier struct {
	endpoint         string
	application, env string
	issuer, clientID string
	client           *http.Client
}

type BrokerVerificationOptions struct {
	PlatformBaseURL, ApplicationCode, EnvironmentCode, Issuer, ClientID string
	HTTPClient                                                          *http.Client
}

func NewPlatformBrokerVerifier(options BrokerVerificationOptions) (*platformBrokerVerifier, error) {
	for name, value := range map[string]string{
		"platform base URL": options.PlatformBaseURL, "application code": options.ApplicationCode,
		"environment code": options.EnvironmentCode, "OIDC issuer": options.Issuer, "OIDC client ID": options.ClientID,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("broker verification %s is required and must be trimmed", name)
		}
	}
	base, err := url.ParseRequestURI(options.PlatformBaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("broker verification platform base URL must be an HTTP(S) origin")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &platformBrokerVerifier{
		endpoint:    strings.TrimRight(options.PlatformBaseURL, "/") + "/api/v1/keycloak/broker-login-verifications",
		application: options.ApplicationCode, env: options.EnvironmentCode,
		issuer: strings.TrimRight(options.Issuer, "/"), clientID: options.ClientID, client: &clientCopy,
	}, nil
}

func (v *platformBrokerVerifier) Verify(ctx context.Context, claims verifiedClaims) error {
	if v == nil || strings.TrimSpace(claims.IdentityID) == "" || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.AccessToken) == "" {
		return errors.New("broker verification requires a verified identity and platform access token")
	}
	body, err := json.Marshal(struct {
		ApplicationCode string `json:"application_code"`
		Environment     string `json:"environment"`
		IdentityID      string `json:"identity_id"`
		Issuer          string `json:"issuer"`
		ClientID        string `json:"client_id"`
	}{v.application, v.env, claims.IdentityID, v.issuer, v.clientID})
	if err != nil {
		return fmt.Errorf("encode broker verification: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create broker verification request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+claims.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("send broker verification: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("broker verification returned HTTP %d", response.StatusCode)
	}
	return nil
}
