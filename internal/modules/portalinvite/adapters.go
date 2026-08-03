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
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"gorm.io/gorm"
)

const maxIntegrationResponseBytes = 64 << 10

// CustomerAdapter 是 CM-004 的防腐层：沿用客户读取的数据范围，只返回唯一注册联系人投影，
// 不向门户邀请模块暴露客户 GORM 模型。
type CustomerAdapter struct {
	db    *gorm.DB
	codec *security.SensitiveCodec
}

func NewCustomerAdapter(db *gorm.DB, codec *security.SensitiveCodec) *CustomerAdapter {
	return &CustomerAdapter{db: db, codec: codec}
}

func (a *CustomerAdapter) CanAccessCustomer(ctx context.Context, principal auth.Principal, customerID uint64) (bool, error) {
	var count int64
	query := a.db.WithContext(ctx).Table("crm_customers AS c").
		Where("c.tenant_id = ? AND c.id = ? AND c.status = ? AND c.merged_into_id IS NULL AND c.deleted_at IS NULL", principal.TenantID, customerID, "ACTIVE")
	switch principal.ScopeMode {
	case auth.ScopeAll:
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return false, nil
		}
		query = query.Where("c.owner_org_id IN ?", principal.OrganizationIDs)
	default:
		query = query.Where("c.owner_user_id = ?", principal.UserID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count == 1, nil
}

func (a *CustomerAdapter) RegistrationContact(ctx context.Context, principal auth.Principal, customerID uint64) (ContactIdentity, error) {
	type row struct {
		TenantID, CustomerName, OwnerUserID, OwnerOrgID string
		CustomerID, ContactID                           uint64
		DisplayName, PhoneMasked, EmailMasked           string
		PhoneCipher, EmailCipher                        []byte
	}
	query := scopedContactQuery(database.FromContext(ctx, a.db), principal, customerID).
		Select("c.tenant_id,c.id AS customer_id,c.name AS customer_name,c.owner_user_id,c.owner_org_id,ct.id AS contact_id,ct.name AS display_name,ct.phone_cipher,ct.phone_masked,ct.email_cipher,ct.email_masked").
		Limit(2)
	var rows []row
	if err := query.Find(&rows).Error; err != nil {
		return ContactIdentity{}, err
	}
	if len(rows) != 1 {
		return ContactIdentity{}, ErrContactInvalid
	}
	phone, err := a.codec.Decrypt(rows[0].PhoneCipher)
	if err != nil {
		return ContactIdentity{}, err
	}
	email, err := a.codec.Decrypt(rows[0].EmailCipher)
	if err != nil {
		return ContactIdentity{}, err
	}
	return ContactIdentity{TenantID: rows[0].TenantID, CustomerName: rows[0].CustomerName, CustomerID: rows[0].CustomerID, ContactID: rows[0].ContactID, DisplayName: rows[0].DisplayName, Phone: phone, Email: email, PhoneMasked: rows[0].PhoneMasked, EmailMasked: rows[0].EmailMasked}, nil
}

func scopedContactQuery(db *gorm.DB, principal auth.Principal, customerID uint64) *gorm.DB {
	query := db.Table("crm_customers AS c").
		Joins("JOIN crm_customer_contacts AS ct ON ct.customer_id = c.id AND ct.tenant_id = c.tenant_id AND ct.deleted_at IS NULL AND ct.is_registration = TRUE").
		Where("c.tenant_id = ? AND c.id = ? AND c.status = ? AND c.deleted_at IS NULL", principal.TenantID, customerID, "ACTIVE")
	switch principal.ScopeMode {
	case auth.ScopeAll:
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("c.owner_org_id IN ?", principal.OrganizationIDs)
		}
	default:
		query = query.Where("c.owner_user_id = ?", principal.UserID)
	}
	return query
}

// 平台外部身份合同未配置时，在生成本地令牌前就拒绝邀请；不能虚构接口路径或 OIDC 主体绕过身份边界。
type UnavailablePlatformProvisioner struct{}

func (UnavailablePlatformProvisioner) ProvisionExternalUser(context.Context, ContactIdentity) (ProvisionedIdentity, error) {
	return ProvisionedIdentity{}, errors.New("platform external-user management OpenAPI is not configured")
}
func (UnavailablePlatformProvisioner) AssignPortalRole(context.Context, string) error {
	return errors.New("platform application-role management OpenAPI is not configured")
}
func (UnavailablePlatformProvisioner) AssignPortalRoleIdempotent(context.Context, string, string) error {
	return errors.New("platform application-role management OpenAPI is not configured")
}

type HTTPPortalProvisioner struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

type HTTPPortalMappingDisabler struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

type PortalMappingDisablerOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	TLS                                               integrationhttp.TLSOptions
	HTTPClient                                        *http.Client
	Now                                               func() time.Time
	NonceReader                                       io.Reader
}

type PortalProvisionerOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	TLS                                               integrationhttp.TLSOptions
	HTTPClient                                        *http.Client
	Now                                               func() time.Time
	NonceReader                                       io.Reader
}

// 门户映射使用 CRM 专用机器身份，令牌合同固定为 client_secret_basic、
// 标准 client_credentials 授权和单一最小 scope。
func NewHTTPPortalProvisioner(ctx context.Context, options PortalProvisionerOptions) (*HTTPPortalProvisioner, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("Portal provision %s is required", name)
		}
	}
	if options.Scope != "portal.identity_mapping.provision" {
		return nil, errors.New("Portal provision machine scope is invalid")
	}
	if !validPortalIntegrationURL(options.Endpoint) || !validPortalIntegrationURL(options.TokenURL) {
		return nil, errors.New("Portal provision endpoint is invalid")
	}
	if err := options.TLS.ValidateEndpoints(options.TokenURL, options.Endpoint); err != nil {
		return nil, fmt.Errorf("Portal provision TLS configuration: %w", err)
	}
	var transport http.RoundTripper
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	} else {
		built, err := integrationhttp.NewTransport(options.TLS, 3*time.Second)
		if err != nil {
			return nil, fmt.Errorf("Portal provision HTTP transport: %w", err)
		}
		transport = built
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL,
		Scopes: []string{options.Scope}, AuthStyle: oauth2.AuthStyleInHeader,
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
	return &HTTPPortalProvisioner{endpoint: options.Endpoint, client: client, now: now, nonceReader: nonceReader}, nil
}

func NewHTTPPortalMappingDisabler(ctx context.Context, options PortalMappingDisablerOptions) (*HTTPPortalMappingDisabler, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("Portal disable %s is required", name)
		}
	}
	if options.Scope != "portal.identity_mapping.disable" || !validPortalIntegrationURL(options.Endpoint) || !validPortalIntegrationURL(options.TokenURL) {
		return nil, errors.New("Portal disable machine contract is invalid")
	}
	if err := options.TLS.ValidateEndpoints(options.TokenURL, options.Endpoint); err != nil {
		return nil, fmt.Errorf("Portal disable TLS configuration: %w", err)
	}
	var transport http.RoundTripper
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	} else {
		built, err := integrationhttp.NewTransport(options.TLS, 3*time.Second)
		if err != nil {
			return nil, fmt.Errorf("Portal disable HTTP transport: %w", err)
		}
		transport = built
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: rejectIntegrationRedirect}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL, Scopes: []string{options.Scope}, AuthStyle: oauth2.AuthStyleInHeader}
	client := &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport}, Timeout: 10 * time.Second, CheckRedirect: rejectIntegrationRedirect}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &HTTPPortalMappingDisabler{endpoint: options.Endpoint, client: client, now: now, nonceReader: nonceReader}, nil
}

func rejectIntegrationRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func (a *HTTPPortalMappingDisabler) DisableMapping(ctx context.Context, tenantID string, customerID uint64, platformUserID, reason, idempotencyKey string) error {
	if a == nil || a.client == nil || !validPortalIntegrationString(tenantID, 64) || customerID == 0 || !validPortalIntegrationString(platformUserID, 128) ||
		!validPortalIntegrationString(strings.TrimSpace(reason), 500) || !validPortalIntegrationString(strings.TrimSpace(idempotencyKey), 128) {
		return errors.New("Portal disable request is invalid")
	}
	body := map[string]any{"tenant_id": tenantID, "customer_id": customerID, "platform_user_id": platformUserID, "reason": strings.TrimSpace(reason)}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", strings.TrimSpace(idempotencyKey))
	request.Header.Set("X-Integration-Timestamp", a.now().UTC().Format(time.RFC3339Nano))
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(a.nonceReader, nonce); err != nil {
		return errors.New("generate Portal disable request nonce failed")
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := a.client.Do(request)
	if err != nil {
		return errors.New("Portal disable transport failed")
	}
	defer response.Body.Close()
	rawResponse, err := io.ReadAll(io.LimitReader(response.Body, maxIntegrationResponseBytes+1))
	if err != nil || len(rawResponse) > maxIntegrationResponseBytes || response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("Portal disable endpoint returned HTTP %d or an invalid response", response.StatusCode)
	}
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Data      struct {
			CustomerID     uint64 `json:"customer_id"`
			PlatformUserID string `json:"platform_user_id"`
			Status         string `json:"status"`
			Version        uint64 `json:"version"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(rawResponse))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Code != "OK" ||
		!validPortalIntegrationString(envelope.RequestID, 128) || envelope.Data.CustomerID != customerID || envelope.Data.PlatformUserID != platformUserID || envelope.Data.Status != "DISABLED" || envelope.Data.Version == 0 {
		return errors.New("Portal disable endpoint returned an invalid response")
	}
	return nil
}

func (a *HTTPPortalProvisioner) ProvisionMapping(ctx context.Context, contact ContactIdentity, identity ProvisionedIdentity) (PortalMapping, error) {
	return a.ProvisionMappingIdempotent(ctx, contact, identity, portalMappingIdempotencyKey(contact, identity))
}

// 补偿任务以不透明且稳定的任务编号作为幂等键，不在请求头暴露客户身份信息。
func (a *HTTPPortalProvisioner) ProvisionMappingIdempotent(ctx context.Context, contact ContactIdentity, identity ProvisionedIdentity, idempotencyKey string) (PortalMapping, error) {
	if a == nil || a.endpoint == "" || a.client == nil || !validPortalMappingIdentity(contact, identity) {
		return PortalMapping{}, errors.New("Portal provision machine client is not configured")
	}
	body := map[string]any{"tenant_id": contact.TenantID, "account_no": identity.AccountNo, "platform_user_id": identity.PlatformUserID, "display_name": contact.DisplayName, "customer_id": contact.CustomerID, "contact_id": contact.ContactID}
	raw, err := json.Marshal(body)
	if err != nil {
		return PortalMapping{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(raw))
	if err != nil {
		return PortalMapping{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validPortalIntegrationString(idempotencyKey, 128) {
		return PortalMapping{}, errors.New("Portal provision idempotency key is invalid")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("X-Integration-Timestamp", a.now().UTC().Format(time.RFC3339Nano))
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(a.nonceReader, nonce); err != nil {
		return PortalMapping{}, errors.New("generate Portal provision request nonce failed")
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := a.client.Do(request)
	if err != nil {
		return PortalMapping{}, errors.New("Portal provision transport failed")
	}
	defer response.Body.Close()
	rawResponse, readErr := io.ReadAll(io.LimitReader(response.Body, maxIntegrationResponseBytes+1))
	if readErr != nil || len(rawResponse) > maxIntegrationResponseBytes {
		return PortalMapping{}, errors.New("Portal provision endpoint returned an invalid response")
	}
	if response.StatusCode != http.StatusCreated {
		return PortalMapping{}, fmt.Errorf("Portal provision endpoint returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Data      struct {
			PortalAccountID string `json:"portal_account_id"`
			AccountNo       string `json:"account_no"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(rawResponse))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return PortalMapping{}, errors.New("Portal provision endpoint returned an invalid response")
	}
	if envelope.Code != "OK" || !validPortalIntegrationString(envelope.RequestID, 128) ||
		!validPortalIntegrationString(envelope.Data.PortalAccountID, 64) || envelope.Data.AccountNo != identity.AccountNo {
		return PortalMapping{}, errors.New("Portal provision endpoint returned an invalid response")
	}
	return PortalMapping{PortalAccountID: envelope.Data.PortalAccountID}, nil
}

func validPortalIntegrationURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validPortalMappingIdentity(contact ContactIdentity, identity ProvisionedIdentity) bool {
	return validPortalIntegrationString(contact.TenantID, 64) && contact.CustomerID > 0 && contact.ContactID > 0 &&
		validPortalIntegrationString(identity.PlatformUserID, 128) && validPortalIntegrationString(identity.AccountNo, 64) &&
		len([]rune(strings.TrimSpace(contact.DisplayName))) <= 128
}

func portalMappingIdempotencyKey(contact ContactIdentity, identity ProvisionedIdentity) string {
	hash := sha256.New()
	for _, value := range []string{"crm-portal-mapping-v1", contact.TenantID, strconv.FormatUint(contact.CustomerID, 10), strconv.FormatUint(contact.ContactID, 10), identity.PlatformUserID, identity.AccountNo} {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return "crm-" + hex.EncodeToString(hash.Sum(nil))
}

func validPortalIntegrationString(value string, maximumBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len([]byte(value)) <= maximumBytes &&
		strings.IndexFunc(value, func(char rune) bool { return char < 0x20 || char == 0x7f }) < 0
}
