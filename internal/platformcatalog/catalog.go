// Package platformcatalog publishes an application-owned authorization catalog
// to the base platform with a dedicated OAuth client-credentials identity.
package platformcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const syncScope = "authorization.catalog.sync"

// Manifest is the application-owned role and permission catalog. Machine-only
// integration scopes are intentionally excluded: they are assigned to OAuth
// clients, not to browser users.
type Manifest struct {
	Version     string
	Permissions []Permission
	Roles       []Role
	Policy      Policy
}

type Permission struct {
	Code, Name, Action, ResourceCode, ResourceName, RiskLevel string
}

type Role struct {
	Code, Name, Description string
	Permissions             []string
}

type Policy struct {
	MaxEffectiveRoles int
}

// Options identifies the platform endpoint and the application-bound catalog
// publisher credential created during subsystem onboarding.
type Options struct {
	Enabled                                        bool
	BaseURL, ApplicationID, ClientID, ClientSecret string
	HTTPClient                                     *http.Client
}

type catalogPayload struct {
	CatalogVersion       string              `json:"catalog_version"`
	Checksum             string              `json:"checksum"`
	ClaimsRoleConfigHash string              `json:"claims_role_config_hash"`
	Permissions          []catalogPermission `json:"permissions"`
	Roles                []catalogRole       `json:"roles"`
	Policy               catalogPolicy       `json:"policy"`
}

type catalogPermission struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Action       string `json:"action"`
	ResourceCode string `json:"resource_code"`
	ResourceName string `json:"resource_name,omitempty"`
	RiskLevel    string `json:"risk_level"`
}

type catalogRole struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
}

type catalogPolicy struct {
	MaxEffectiveRoles int `json:"max_effective_roles"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

// Publish validates and publishes the manifest. When publication is enabled,
// any error is returned to the caller so the service can fail startup rather
// than accept Claims from a stale or incompatible platform catalog.
func Publish(ctx context.Context, manifest Manifest, options Options) error {
	if !options.Enabled {
		return nil
	}
	payload, err := normalizeManifest(manifest)
	if err != nil {
		return err
	}
	baseURL, err := validateOptions(options)
	if err != nil {
		return err
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// OAuth publisher credentials and bearer tokens must never cross an HTTP
	// redirect boundary, even when a caller injects a custom client.
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &clientCopy
	token, err := requestAccessToken(ctx, client, baseURL, options)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode authorization catalog: %w", err)
	}
	endpoint := strings.TrimRight(baseURL.String(), "/") + "/api/v1/applications/" + url.PathEscape(options.ApplicationID) + "/authorization-catalog"
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create authorization catalog request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("publish authorization catalog: platform transport failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("platform authorization catalog returned HTTP %d", response.StatusCode)
	}
	return nil
}

// ClaimsRoleConfigHash returns the opaque compatibility hash published to the
// platform and expected in this application's OIDC Claims.
func ClaimsRoleConfigHash(manifest Manifest) (string, error) {
	payload, err := normalizeManifest(manifest)
	if err != nil {
		return "", err
	}
	return payload.ClaimsRoleConfigHash, nil
}

// CatalogChecksum returns the deterministic checksum of the complete catalog
// definition sent to the platform. It is separate from the Claims
// compatibility hash, which only tracks role-to-permission mappings.
func CatalogChecksum(manifest Manifest) (string, error) {
	payload, err := normalizeManifest(manifest)
	if err != nil {
		return "", err
	}
	return payload.Checksum, nil
}

// HasPermission and HasRole let authentication validators consume the same
// catalog that is published to the platform, preventing separate allowlists
// from drifting apart.
func HasPermission(manifest Manifest, expected string) bool {
	for _, item := range manifest.Permissions {
		if item.Code == expected {
			return true
		}
	}
	return false
}

func HasRole(manifest Manifest, expected string) bool {
	for _, item := range manifest.Roles {
		if item.Code == expected {
			return true
		}
	}
	return false
}

// ValidateClaimsRoleConfigHash verifies that the runtime OIDC configuration
// expects the exact mapping published by the application binary.
func ValidateClaimsRoleConfigHash(manifest Manifest, configured string) error {
	expected, err := ClaimsRoleConfigHash(manifest)
	if err != nil {
		return err
	}
	if strings.TrimSpace(configured) != expected {
		return fmt.Errorf("OIDC role configuration hash does not match embedded authorization catalog; expected %s", expected)
	}
	return nil
}

func normalizeManifest(manifest Manifest) (catalogPayload, error) {
	manifest.Version = strings.TrimSpace(manifest.Version)
	if manifest.Version == "" || len(manifest.Permissions) == 0 || len(manifest.Roles) == 0 {
		return catalogPayload{}, errors.New("authorization catalog manifest is incomplete")
	}
	if manifest.Policy.MaxEffectiveRoles < 0 || manifest.Policy.MaxEffectiveRoles > 10 {
		return catalogPayload{}, errors.New("authorization catalog max_effective_roles must be between 0 and 10")
	}
	permissionCodes := make(map[string]struct{}, len(manifest.Permissions))
	resourceActions := make(map[string]struct{}, len(manifest.Permissions))
	payload := catalogPayload{CatalogVersion: manifest.Version, Policy: catalogPolicy{MaxEffectiveRoles: manifest.Policy.MaxEffectiveRoles}}
	for _, item := range manifest.Permissions {
		item.Code, item.Name = strings.TrimSpace(item.Code), strings.TrimSpace(item.Name)
		item.Action, item.ResourceCode = strings.TrimSpace(item.Action), strings.TrimSpace(item.ResourceCode)
		item.ResourceName, item.RiskLevel = strings.TrimSpace(item.ResourceName), strings.ToUpper(strings.TrimSpace(item.RiskLevel))
		if item.Code == "" || item.Name == "" || item.Action == "" || item.ResourceCode == "" {
			return catalogPayload{}, errors.New("authorization catalog permission is incomplete")
		}
		if item.RiskLevel != "LOW" && item.RiskLevel != "MEDIUM" && item.RiskLevel != "HIGH" && item.RiskLevel != "CRITICAL" {
			return catalogPayload{}, fmt.Errorf("authorization catalog permission %s has invalid risk level", item.Code)
		}
		if _, duplicate := permissionCodes[item.Code]; duplicate {
			return catalogPayload{}, fmt.Errorf("authorization catalog contains duplicate permission %s", item.Code)
		}
		resourceAction := item.ResourceCode + "\x00" + strings.ToLower(item.Action)
		if _, duplicate := resourceActions[resourceAction]; duplicate {
			return catalogPayload{}, fmt.Errorf("authorization catalog contains duplicate resource/action %s/%s", item.ResourceCode, item.Action)
		}
		permissionCodes[item.Code], resourceActions[resourceAction] = struct{}{}, struct{}{}
		payload.Permissions = append(payload.Permissions, catalogPermission{Code: item.Code, Name: item.Name, Action: item.Action, ResourceCode: item.ResourceCode, ResourceName: item.ResourceName, RiskLevel: item.RiskLevel})
	}
	sort.Slice(payload.Permissions, func(i, j int) bool { return payload.Permissions[i].Code < payload.Permissions[j].Code })
	roleCodes := make(map[string]struct{}, len(manifest.Roles))
	for _, item := range manifest.Roles {
		item.Code, item.Name, item.Description = strings.TrimSpace(item.Code), strings.TrimSpace(item.Name), strings.TrimSpace(item.Description)
		if item.Code == "" || item.Name == "" {
			return catalogPayload{}, errors.New("authorization catalog role is incomplete")
		}
		if _, duplicate := roleCodes[item.Code]; duplicate {
			return catalogPayload{}, fmt.Errorf("authorization catalog contains duplicate role %s", item.Code)
		}
		roleCodes[item.Code] = struct{}{}
		permissions := sortedUnique(item.Permissions)
		for _, permission := range permissions {
			if _, exists := permissionCodes[permission]; !exists {
				return catalogPayload{}, fmt.Errorf("authorization catalog role %s references unknown permission %s", item.Code, permission)
			}
		}
		payload.Roles = append(payload.Roles, catalogRole{Code: item.Code, Name: item.Name, Description: item.Description, Permissions: permissions})
	}
	sort.Slice(payload.Roles, func(i, j int) bool { return payload.Roles[i].Code < payload.Roles[j].Code })
	catalogCanonical := struct {
		Version     string              `json:"catalog_version"`
		Permissions []catalogPermission `json:"permissions"`
		Roles       []catalogRole       `json:"roles"`
		Policy      catalogPolicy       `json:"policy"`
	}{manifest.Version, payload.Permissions, payload.Roles, payload.Policy}
	catalogEncoded, err := json.Marshal(catalogCanonical)
	if err != nil {
		return catalogPayload{}, fmt.Errorf("encode authorization catalog checksum: %w", err)
	}
	catalogSum := sha256.Sum256(catalogEncoded)
	payload.Checksum = "sha256:" + hex.EncodeToString(catalogSum[:])
	// Claims compatibility is bound to stable role codes and their permission
	// mappings. Chinese display names and descriptions are presentation metadata
	// and must not invalidate every active session when copy is edited.
	type claimsRole struct {
		Code        string   `json:"code"`
		Permissions []string `json:"permissions"`
	}
	canonical := struct {
		Roles []claimsRole `json:"roles"`
	}{Roles: make([]claimsRole, 0, len(payload.Roles))}
	for _, item := range payload.Roles {
		canonical.Roles = append(canonical.Roles, claimsRole{Code: item.Code, Permissions: item.Permissions})
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return catalogPayload{}, fmt.Errorf("encode authorization Claims compatibility mapping: %w", err)
	}
	sum := sha256.Sum256(encoded)
	payload.ClaimsRoleConfigHash = "sha256:" + hex.EncodeToString(sum[:])
	return payload, nil
}

func validateOptions(options Options) (*url.URL, error) {
	if strings.TrimSpace(options.BaseURL) == "" || strings.TrimSpace(options.ApplicationID) == "" || strings.TrimSpace(options.ClientID) == "" || options.ClientSecret == "" {
		return nil, errors.New("authorization catalog synchronization configuration is incomplete")
	}
	parsed, err := url.ParseRequestURI(options.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("authorization catalog platform base URL must be an HTTP(S) origin")
	}
	return parsed, nil
}

func requestAccessToken(ctx context.Context, client *http.Client, baseURL *url.URL, options Options) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {syncScope}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL.String(), "/")+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create authorization catalog token request: %w", err)
	}
	request.SetBasicAuth(options.ClientID, options.ClientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("request authorization catalog token: platform transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return "", fmt.Errorf("platform authorization catalog token returned HTTP %d", response.StatusCode)
	}
	var token tokenResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&token); err != nil {
		return "", errors.New("decode authorization catalog token: invalid platform response")
	}
	if strings.TrimSpace(token.AccessToken) == "" || !strings.EqualFold(token.TokenType, "bearer") || !containsScope(token.Scope, syncScope) {
		return "", errors.New("platform token is missing authorization.catalog.sync scope")
	}
	return token.AccessToken, nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsScope(value, expected string) bool {
	for _, scope := range strings.Fields(value) {
		if scope == expected {
			return true
		}
	}
	return false
}
