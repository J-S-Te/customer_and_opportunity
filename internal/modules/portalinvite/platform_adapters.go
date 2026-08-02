package portalinvite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	externalUserProvisionScope = "external_user.provision"
	applicationRoleAssignScope = "application_role.assign"
	applicationRoleRevokeScope = "application_role.revoke"
	portalApplicationRole      = "portal_customer"
)

// HTTPPlatformProvisioner is the CRM anti-corruption adapter for the platform
// external-identity management contract. User provisioning and role assignment
// deliberately use different OAuth clients so neither credential can exercise
// both write capabilities.
type HTTPPlatformProvisioner struct {
	provisionURL string
	application  string
	provision    *http.Client
	roles        *HTTPPlatformRoleAssigner
	now          func() time.Time
	nonceReader  io.Reader
}

type PlatformProvisionerOptions struct {
	ProvisionURL, RoleAssignURL, TokenURL, ApplicationCode   string
	ProvisionClientID, ProvisionClientSecret, ProvisionScope string
	RoleClientID, RoleClientSecret, RoleScope                string
	TLS                                                      integrationhttp.TLSOptions
	HTTPClient                                               *http.Client
	Now                                                      func() time.Time
	NonceReader                                              io.Reader
}

// HTTPPlatformRoleAssigner is exported for the compensation worker, which
// retries only the already-recorded role assignment and therefore must not
// receive the external-user provisioning credential.
type HTTPPlatformRoleAssigner struct {
	endpoint    string
	application string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

type PlatformRoleAssignerOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope, ApplicationCode string
	TLS                                                                integrationhttp.TLSOptions
	HTTPClient                                                         *http.Client
	Now                                                                func() time.Time
	NonceReader                                                        io.Reader
}

// HTTPPlatformRoleRevoker owns a dedicated application_role.revoke OAuth
// client. It is not embedded in HTTPPlatformProvisioner so invitation
// generation and its forward-recovery worker can never revoke existing Portal
// access with the credentials they already hold.
type HTTPPlatformRoleRevoker struct {
	endpoint    string
	application string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

type PlatformRoleRevokerOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope, ApplicationCode string
	TLS                                                                integrationhttp.TLSOptions
	HTTPClient                                                         *http.Client
	Now                                                                func() time.Time
	NonceReader                                                        io.Reader
}

func NewHTTPPlatformProvisioner(ctx context.Context, options PlatformProvisionerOptions) (*HTTPPlatformProvisioner, error) {
	for name, value := range map[string]string{
		"provision endpoint": options.ProvisionURL, "role endpoint": options.RoleAssignURL,
		"token URL": options.TokenURL, "application code": options.ApplicationCode,
		"provision client ID": options.ProvisionClientID, "provision client secret": options.ProvisionClientSecret,
		"role client ID": options.RoleClientID, "role client secret": options.RoleClientSecret,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("platform external identity %s is required", name)
		}
	}
	if options.ProvisionScope != externalUserProvisionScope || options.RoleScope != applicationRoleAssignScope {
		return nil, errors.New("platform external identity machine scope is invalid")
	}
	if !validPlatformIntegrationURL(options.ProvisionURL) || !validPlatformIntegrationURL(options.RoleAssignURL) || !validPlatformIntegrationURL(options.TokenURL) {
		return nil, errors.New("platform external identity endpoint is invalid")
	}
	transport, err := platformIntegrationTransport(options.HTTPClient, options.TLS, options.TokenURL, options.ProvisionURL, options.RoleAssignURL)
	if err != nil {
		return nil, err
	}
	now, nonceReader := platformIntegrationDependencies(options.Now, options.NonceReader)
	provisionClient := platformOAuthClient(ctx, transport, options.TokenURL, options.ProvisionClientID, options.ProvisionClientSecret, externalUserProvisionScope)
	roleClient := platformOAuthClient(ctx, transport, options.TokenURL, options.RoleClientID, options.RoleClientSecret, applicationRoleAssignScope)
	roles := &HTTPPlatformRoleAssigner{endpoint: options.RoleAssignURL, application: options.ApplicationCode, client: roleClient, now: now, nonceReader: nonceReader}
	return &HTTPPlatformProvisioner{provisionURL: options.ProvisionURL, application: options.ApplicationCode, provision: provisionClient, roles: roles, now: now, nonceReader: nonceReader}, nil
}

func NewHTTPPlatformRoleAssigner(ctx context.Context, options PlatformRoleAssignerOptions) (*HTTPPlatformRoleAssigner, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret, "application code": options.ApplicationCode} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("platform role assignment %s is required", name)
		}
	}
	if options.Scope != applicationRoleAssignScope || !validPlatformIntegrationURL(options.Endpoint) || !validPlatformIntegrationURL(options.TokenURL) {
		return nil, errors.New("platform role assignment configuration is invalid")
	}
	transport, err := platformIntegrationTransport(options.HTTPClient, options.TLS, options.TokenURL, options.Endpoint)
	if err != nil {
		return nil, err
	}
	now, nonceReader := platformIntegrationDependencies(options.Now, options.NonceReader)
	client := platformOAuthClient(ctx, transport, options.TokenURL, options.ClientID, options.ClientSecret, applicationRoleAssignScope)
	return &HTTPPlatformRoleAssigner{endpoint: options.Endpoint, application: options.ApplicationCode, client: client, now: now, nonceReader: nonceReader}, nil
}

func NewHTTPPlatformRoleRevoker(ctx context.Context, options PlatformRoleRevokerOptions) (*HTTPPlatformRoleRevoker, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret, "application code": options.ApplicationCode} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("platform role revocation %s is required", name)
		}
	}
	if options.Scope != applicationRoleRevokeScope || !validPlatformIntegrationURL(options.Endpoint) || !validPlatformIntegrationURL(options.TokenURL) {
		return nil, errors.New("platform role revocation configuration is invalid")
	}
	transport, err := platformIntegrationTransport(options.HTTPClient, options.TLS, options.TokenURL, options.Endpoint)
	if err != nil {
		return nil, err
	}
	now, nonceReader := platformIntegrationDependencies(options.Now, options.NonceReader)
	client := platformOAuthClient(ctx, transport, options.TokenURL, options.ClientID, options.ClientSecret, applicationRoleRevokeScope)
	return &HTTPPlatformRoleRevoker{endpoint: options.Endpoint, application: options.ApplicationCode, client: client, now: now, nonceReader: nonceReader}, nil
}

func (p *HTTPPlatformProvisioner) ProvisionExternalUser(ctx context.Context, contact ContactIdentity) (ProvisionedIdentity, error) {
	if p == nil || p.provision == nil || !validPlatformContactIdentity(contact) {
		return ProvisionedIdentity{}, errors.New("platform external user request is invalid")
	}
	payload := struct {
		DisplayName string `json:"display_name"`
		Mobile      string `json:"mobile,omitempty"`
		Email       string `json:"email,omitempty"`
	}{DisplayName: strings.TrimSpace(contact.DisplayName), Mobile: strings.TrimSpace(contact.Phone), Email: strings.TrimSpace(contact.Email)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ProvisionedIdentity{}, err
	}
	idempotencyKey := platformIdempotencyKey("external-user", contact.TenantID, fmt.Sprint(contact.CustomerID), fmt.Sprint(contact.ContactID), p.application)
	request, err := newPlatformIntegrationPOST(ctx, p.provisionURL, idempotencyKey, raw, p.now, p.nonceReader)
	if err != nil {
		return ProvisionedIdentity{}, err
	}
	var envelope platformProvisionEnvelope
	if err = doStrictPlatformJSON(p.provision, request, http.StatusOK, &envelope); err != nil {
		return ProvisionedIdentity{}, err
	}
	if envelope.Code != "OK" || !validPlatformRequestID(envelope.RequestID) || envelope.Data.PlatformUserID == "" || envelope.Data.AccountNo == "" ||
		envelope.Data.PlatformUserID != strings.TrimSpace(envelope.Data.PlatformUserID) || envelope.Data.AccountNo != strings.TrimSpace(envelope.Data.AccountNo) ||
		len(envelope.Data.PlatformUserID) > 128 || len(envelope.Data.AccountNo) > 64 {
		return ProvisionedIdentity{}, errors.New("platform external user endpoint returned an invalid response")
	}
	return ProvisionedIdentity{PlatformUserID: envelope.Data.PlatformUserID, AccountNo: envelope.Data.AccountNo}, nil
}

func (p *HTTPPlatformProvisioner) AssignPortalRole(ctx context.Context, platformUserID string) error {
	if p == nil || p.roles == nil {
		return errors.New("platform role assignment client is not configured")
	}
	return p.roles.AssignPortalRole(ctx, platformUserID, "")
}

func (p *HTTPPlatformProvisioner) AssignPortalRoleIdempotent(ctx context.Context, platformUserID, idempotencyKey string) error {
	if p == nil || p.roles == nil {
		return errors.New("platform role assignment client is not configured")
	}
	return p.roles.AssignPortalRole(ctx, platformUserID, idempotencyKey)
}

func (p *HTTPPlatformRoleAssigner) AssignPortalRole(ctx context.Context, platformUserID, idempotencyKey string) error {
	platformUserID = strings.TrimSpace(platformUserID)
	if p == nil || p.client == nil || platformUserID == "" || len(platformUserID) > 128 {
		return errors.New("platform role assignment request is invalid")
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		idempotencyKey = platformIdempotencyKey("portal-role", platformUserID, p.application, portalApplicationRole)
	}
	payload := struct {
		PlatformUserID  string `json:"platform_user_id"`
		ApplicationCode string `json:"application_code"`
		RoleCode        string `json:"role_code"`
	}{PlatformUserID: platformUserID, ApplicationCode: p.application, RoleCode: portalApplicationRole}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := newPlatformIntegrationPOST(ctx, p.endpoint, idempotencyKey, raw, p.now, p.nonceReader)
	if err != nil {
		return err
	}
	var envelope platformRoleEnvelope
	if err = doStrictPlatformJSON(p.client, request, http.StatusOK, &envelope); err != nil {
		return err
	}
	if envelope.Code != "OK" || !validPlatformRequestID(envelope.RequestID) || envelope.Data.PlatformUserID != platformUserID ||
		envelope.Data.ApplicationCode != p.application || envelope.Data.RoleCode != portalApplicationRole || envelope.Data.Status != "ACTIVE" {
		return errors.New("platform role assignment endpoint returned an invalid response")
	}
	return nil
}

func (p *HTTPPlatformRoleRevoker) RevokePortalRole(ctx context.Context, platformUserID, idempotencyKey string) error {
	platformUserID = strings.TrimSpace(platformUserID)
	if p == nil || p.client == nil || platformUserID == "" || len(platformUserID) > 128 {
		return errors.New("platform role revocation request is invalid")
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		idempotencyKey = platformIdempotencyKey("portal-role-revoke", platformUserID, p.application, portalApplicationRole)
	}
	payload := struct {
		PlatformUserID  string `json:"platform_user_id"`
		ApplicationCode string `json:"application_code"`
		RoleCode        string `json:"role_code"`
	}{PlatformUserID: platformUserID, ApplicationCode: p.application, RoleCode: portalApplicationRole}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := newPlatformIntegrationPOST(ctx, p.endpoint, idempotencyKey, raw, p.now, p.nonceReader)
	if err != nil {
		return err
	}
	var envelope platformRoleEnvelope
	if err = doStrictPlatformJSON(p.client, request, http.StatusOK, &envelope); err != nil {
		return err
	}
	if envelope.Code != "OK" || !validPlatformRequestID(envelope.RequestID) || envelope.Data.PlatformUserID != platformUserID ||
		envelope.Data.ApplicationCode != p.application || envelope.Data.RoleCode != portalApplicationRole || envelope.Data.Status != "DISABLED" {
		return errors.New("platform role revocation endpoint returned an invalid response")
	}
	return nil
}

type platformProvisionEnvelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      struct {
		PlatformUserID string `json:"platform_user_id"`
		AccountNo      string `json:"account_no"`
	} `json:"data"`
}

type platformRoleEnvelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      struct {
		PlatformUserID  string `json:"platform_user_id"`
		ApplicationCode string `json:"application_code"`
		RoleCode        string `json:"role_code"`
		Status          string `json:"status"`
	} `json:"data"`
}

func validPlatformContactIdentity(contact ContactIdentity) bool {
	return strings.TrimSpace(contact.TenantID) != "" && contact.TenantID == strings.TrimSpace(contact.TenantID) && contact.CustomerID > 0 && contact.ContactID > 0 &&
		strings.TrimSpace(contact.DisplayName) != "" && len([]rune(strings.TrimSpace(contact.DisplayName))) <= 128 &&
		(strings.TrimSpace(contact.Phone) != "" || strings.TrimSpace(contact.Email) != "")
}

func platformIdempotencyKey(parts ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("crm-platform-v1"))
	for _, part := range parts {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strings.TrimSpace(part)))
	}
	return "crm-" + hex.EncodeToString(hash.Sum(nil))
}

func platformIntegrationDependencies(now func() time.Time, nonceReader io.Reader) (func() time.Time, io.Reader) {
	if now == nil {
		now = time.Now
	}
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return now, nonceReader
}

func platformIntegrationTransport(injected *http.Client, tlsOptions integrationhttp.TLSOptions, endpoints ...string) (http.RoundTripper, error) {
	if err := tlsOptions.ValidateEndpoints(endpoints...); err != nil {
		return nil, err
	}
	if injected != nil && injected.Transport != nil {
		return injected.Transport, nil
	}
	return integrationhttp.NewTransport(tlsOptions, 3*time.Second)
}

func platformOAuthClient(ctx context.Context, transport http.RoundTripper, tokenURL, clientID, clientSecret, scope string) *http.Client {
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: rejectPlatformIntegrationRedirect}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{ClientID: clientID, ClientSecret: clientSecret, TokenURL: tokenURL, Scopes: []string{scope}, AuthStyle: oauth2.AuthStyleInHeader}
	return &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport}, Timeout: 10 * time.Second, CheckRedirect: rejectPlatformIntegrationRedirect}
}

func rejectPlatformIntegrationRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func newPlatformIntegrationPOST(ctx context.Context, endpoint, idempotencyKey string, raw []byte, now func() time.Time, nonceReader io.Reader) (*http.Request, error) {
	if idempotencyKey = strings.TrimSpace(idempotencyKey); idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, errors.New("platform integration idempotency key is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("X-Integration-Timestamp", now().UTC().Format(time.RFC3339Nano))
	if requestID := strings.TrimSpace(requestctx.ID(ctx)); requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(nonceReader, nonce); err != nil {
		return nil, errors.New("generate platform integration request nonce failed")
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	return request, nil
}

func doStrictPlatformJSON(client *http.Client, request *http.Request, expectedStatus int, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return errors.New("platform management transport failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxIntegrationResponseBytes+1))
	if err != nil || len(raw) > maxIntegrationResponseBytes {
		return errors.New("platform management endpoint returned an invalid response")
	}
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("platform management endpoint returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return errors.New("platform management endpoint returned an invalid response")
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("platform management endpoint returned an invalid response")
	}
	return nil
}

func validPlatformRequestID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}

func validPlatformIntegrationURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}
