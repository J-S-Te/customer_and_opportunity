package portalbootstrap

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
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const maxAdapterResponseBytes = 64 << 10

// OIDCAdapter follows the platform's deployed Discovery/JWKS contract. Token
// claims are returned only after signature, issuer, audience, expiry and nonce
// validation performed by go-oidc and the explicit platform checks below.
type OIDCAdapter struct {
	config     oauth2.Config
	verifier   *oidc.IDTokenVerifier
	provider   *oidc.Provider
	httpClient *http.Client
}

func NewOIDCAdapter(ctx context.Context, config Config) (*OIDCAdapter, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if config.OIDCBackchannelURL != "" {
		publicURL, err := url.Parse(strings.TrimRight(config.OIDCIssuer, "/"))
		if err != nil {
			return nil, fmt.Errorf("parse OIDC issuer: %w", err)
		}
		backchannelURL, err := url.Parse(strings.TrimRight(config.OIDCBackchannelURL, "/"))
		if err != nil {
			return nil, fmt.Errorf("parse OIDC backchannel: %w", err)
		}
		httpClient.Transport = &issuerTransport{base: http.DefaultTransport, public: publicURL, backchannel: backchannelURL}
	}
	oidcContext := oidc.ClientContext(ctx, httpClient)
	provider, err := oidc.NewProvider(oidcContext, strings.TrimRight(config.OIDCIssuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("load OIDC discovery: %w", err)
	}
	return &OIDCAdapter{
		config:   oauth2.Config{ClientID: config.OIDCClientID, ClientSecret: config.OIDCClientSecret, Endpoint: provider.Endpoint(), RedirectURL: config.OIDCRedirectURI, Scopes: config.OIDCScopes},
		verifier: provider.Verifier(&oidc.Config{ClientID: config.OIDCClientID}), provider: provider, httpClient: httpClient,
	}, nil
}

func (a *OIDCAdapter) AuthorizationURL(state, nonce, codeChallenge, _ string) (string, error) {
	if state == "" || nonce == "" || codeChallenge == "" {
		return "", errors.New("OIDC login parameters are incomplete")
	}
	return a.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.SetAuthURLParam("code_challenge", codeChallenge), oauth2.SetAuthURLParam("code_challenge_method", "S256")), nil
}

func (a *OIDCAdapter) ExchangeAndValidate(ctx context.Context, code, verifier, nonce string) (account.Claims, error) {
	if strings.TrimSpace(code) == "" || verifier == "" || nonce == "" {
		return account.Claims{}, errors.New("OIDC callback parameters are incomplete")
	}
	oidcContext := oidc.ClientContext(ctx, a.httpClient)
	token, err := a.config.Exchange(oidcContext, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return account.Claims{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return account.Claims{}, errors.New("OIDC response has no ID token")
	}
	idToken, err := a.verifier.Verify(oidcContext, rawIDToken)
	if err != nil {
		return account.Claims{}, fmt.Errorf("verify ID token: %w", err)
	}
	var claims struct {
		Subject, Nonce, TenantID, RoleConfigHash, TokenUse string
		Roles, Permissions                                 []string
		AuthzRevision                                      uint64
	}
	var raw struct {
		Subject        string   `json:"sub"`
		Nonce          string   `json:"nonce"`
		TenantID       string   `json:"tenant_id"`
		Roles          []string `json:"roles"`
		Permissions    []string `json:"permissions"`
		RoleConfigHash string   `json:"role_config_hash"`
		AuthzRevision  uint64   `json:"authz_revision"`
		TokenUse       string   `json:"token_use"`
	}
	if err := idToken.Claims(&raw); err != nil {
		return account.Claims{}, fmt.Errorf("decode ID token claims: %w", err)
	}
	claims.Subject, claims.Nonce, claims.TenantID, claims.Roles, claims.Permissions = raw.Subject, raw.Nonce, raw.TenantID, raw.Roles, raw.Permissions
	claims.RoleConfigHash, claims.AuthzRevision, claims.TokenUse = raw.RoleConfigHash, raw.AuthzRevision, raw.TokenUse
	if claims.Nonce != nonce || claims.TokenUse != "id_token" || strings.TrimSpace(claims.Subject) == "" || claims.Subject != strings.TrimSpace(claims.Subject) || claims.TenantID == "" || claims.RoleConfigHash == "" || claims.AuthzRevision == 0 || len(claims.Roles) == 0 || len(claims.Permissions) == 0 {
		return account.Claims{}, errors.New("OIDC authorization claims are invalid")
	}
	return account.Claims{Subject: claims.Subject, TenantID: claims.TenantID, Roles: claims.Roles, Permissions: claims.Permissions, RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision, ExpiresAt: earliestExpiry(idToken.Expiry, token.Expiry), AccessToken: token.AccessToken}, nil
}

func (a *OIDCAdapter) UserInfo(ctx context.Context, accessToken string) (account.Claims, error) {
	if strings.TrimSpace(accessToken) == "" {
		return account.Claims{}, errors.New("OIDC access token is missing")
	}
	oidcContext := oidc.ClientContext(ctx, a.httpClient)
	info, err := a.provider.UserInfo(oidcContext, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken, TokenType: "Bearer"}))
	if err != nil {
		return account.Claims{}, errors.New("OIDC UserInfo verification failed")
	}
	var claims struct {
		Subject        string   `json:"sub"`
		TenantID       string   `json:"tenant_id"`
		Roles          []string `json:"roles"`
		Permissions    []string `json:"permissions"`
		RoleConfigHash string   `json:"role_config_hash"`
		AuthzRevision  uint64   `json:"authz_revision"`
	}
	if err = info.Claims(&claims); err != nil {
		return account.Claims{}, errors.New("OIDC UserInfo response is invalid")
	}
	return account.Claims{Subject: claims.Subject, TenantID: claims.TenantID, Roles: claims.Roles, Permissions: claims.Permissions, RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision}, nil
}

type issuerTransport struct {
	base                http.RoundTripper
	public, backchannel *url.URL
}

func (t *issuerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != t.public.Scheme || request.URL.Host != t.public.Host {
		return t.base.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host = t.backchannel.Scheme, t.backchannel.Host
	return t.base.RoundTrip(clone)
}

type SecretProtector struct{ codec *AEADCodec }

func NewSecretProtector(key []byte) (*SecretProtector, error) {
	codec, err := NewAEADCodec(key)
	if err != nil {
		return nil, err
	}
	return &SecretProtector{codec: codec}, nil
}
func (p *SecretProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	return p.codec.Encrypt(value)
}
func (p *SecretProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) {
	return p.codec.Decrypt(value)
}

type CRMInviteClientOptions struct {
	BaseURL, TokenURL, ClientID, ClientSecret, Scope string
	HTTPClient                                       *http.Client
	Now                                              func() time.Time
	NonceReader                                      io.Reader
}

type CRMInviteClient struct {
	baseURL     string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

// NewCRMInviteClient builds the Portal's dedicated client-credentials caller.
// Audience is deliberately not sent to the token endpoint: the deployed base
// platform signs all application tokens with its deployment-wide application
// JWT audience, which CRM independently verifies from its own configuration.
func NewCRMInviteClient(ctx context.Context, options CRMInviteClientOptions) (*CRMInviteClient, error) {
	for name, value := range map[string]string{"base URL": options.BaseURL, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("CRM invitation %s is required", name)
		}
	}
	if strings.TrimSpace(options.Scope) != "portal.invite.verify" {
		return nil, errors.New("CRM invitation machine scope is invalid")
	}
	baseTransport := http.DefaultTransport
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		baseTransport = options.HTTPClient.Transport
	}
	tokenHTTPClient := &http.Client{Transport: baseTransport, Timeout: 5 * time.Second}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenHTTPClient)
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret,
		TokenURL: options.TokenURL, Scopes: []string{options.Scope},
		AuthStyle: oauth2.AuthStyleInHeader,
	}
	client := &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: baseTransport}, Timeout: 10 * time.Second}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &CRMInviteClient{baseURL: strings.TrimRight(options.BaseURL, "/"), client: client, now: now, nonceReader: nonceReader}, nil
}
func (c *CRMInviteClient) Verify(ctx context.Context, token string) (account.VerifiedInvite, error) {
	var result struct {
		TenantID       string  `json:"tenant_id"`
		PlatformUserID string  `json:"platform_user_id"`
		CustomerID     uint64  `json:"customer_id"`
		ContactID      *uint64 `json:"contact_id"`
	}
	if err := c.post(ctx, "/internal/portal/invites/verify", map[string]string{"token": token}, &result); err != nil {
		return account.VerifiedInvite{}, err
	}
	if strings.TrimSpace(result.TenantID) == "" || strings.TrimSpace(result.PlatformUserID) == "" || result.CustomerID == 0 {
		return account.VerifiedInvite{}, errors.New("CRM invitation endpoint returned an invalid response")
	}
	return account.VerifiedInvite{TenantID: result.TenantID, ExpectedPlatformUserID: result.PlatformUserID, CustomerID: result.CustomerID, ContactID: result.ContactID}, nil
}
func (c *CRMInviteClient) Consume(ctx context.Context, token, subject string) error {
	requestID := requestctx.ID(ctx)
	if requestID == "" {
		requestID = requestctx.NewID()
	}
	return c.post(ctx, "/internal/portal/invites/consume", map[string]string{"token": token, "platform_user_id": subject, "request_id": requestID}, nil)
}
func (c *CRMInviteClient) post(ctx context.Context, path string, body any, target any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return errors.New("encode CRM invitation request failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("create CRM invitation request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Integration-Timestamp", c.now().UTC().Format(time.RFC3339Nano))
	nonce, err := c.newNonce()
	if err != nil {
		return err
	}
	request.Header.Set("X-Integration-Nonce", nonce)
	response, err := c.client.Do(request)
	if err != nil {
		return safeCRMInviteTransportError(err)
	}
	defer response.Body.Close()
	rawResponse, err := io.ReadAll(io.LimitReader(response.Body, maxAdapterResponseBytes+1))
	if err != nil || len(rawResponse) > maxAdapterResponseBytes {
		return errors.New("CRM invitation endpoint returned an invalid response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("CRM invitation endpoint returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code string          `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rawResponse, &envelope); err != nil || envelope.Code != "OK" {
		return errors.New("CRM invitation endpoint returned an invalid response")
	}
	if target != nil && (len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) || json.Unmarshal(envelope.Data, target) != nil) {
		return errors.New("CRM invitation endpoint returned an invalid response")
	}
	return nil
}

func (c *CRMInviteClient) newNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(c.nonceReader, raw); err != nil {
		return "", errors.New("generate CRM invitation request nonce failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func safeCRMInviteTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("CRM invitation request timed out")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("CRM invitation request timed out")
	}
	return errors.New("CRM invitation transport failed")
}

func earliestExpiry(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if !value.IsZero() && (result.IsZero() || value.Before(result)) {
			result = value.UTC()
		}
	}
	return result
}
func hashText(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
