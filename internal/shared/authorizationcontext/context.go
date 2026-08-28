package authorizationcontext

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

const (
	ScopeApplication = "APPLICATION"
	ScopeEnvironment = "ENVIRONMENT"
	ScopeTenant      = "TENANT"
	ScopeOrg         = "ORG"
	ScopeSelf        = "SELF"
	ScopeProject     = "PROJECT"
)

var (
	ErrTokenRejected = errors.New("authorization context rejected the access token")
	ErrForbidden     = errors.New("authorization context denied application access")
	ErrUnavailable   = errors.New("authorization context is unavailable")
)

// Response is the platform authorization-context wire contract. Application
// identity is validated independently from user identity so a valid token for
// another subsystem can never be promoted into the current subsystem session.
type Response struct {
	Subject                    string                 `json:"sub"`
	SubjectID                  string                 `json:"subject_id"`
	IdentityID                 string                 `json:"identity_id"`
	TenantID                   string                 `json:"tenant_id"`
	PersonID                   string                 `json:"person_id"`
	ClientID                   string                 `json:"client_id"`
	ApplicationCode            string                 `json:"application_code"`
	EnvironmentCode            string                 `json:"environment_code"`
	Roles                      []string               `json:"roles"`
	Permissions                []string               `json:"permissions"`
	DataScopes                 []sharedauth.DataScope `json:"data_scopes"`
	CatalogVersion             string                 `json:"catalog_version"`
	CompatibleCatalogVersions  []string               `json:"compatible_catalog_versions"`
	RoleConfigHash             string                 `json:"role_config_hash"`
	CompatibleRoleConfigHashes []string               `json:"compatible_role_config_hashes"`
	AuthorizationRevision      uint64                 `json:"authorization_revision"`
	CustomerRef                string                 `json:"customer_ref,omitempty"`
	UserLoginIP                string                 `json:"user_login_ip,omitempty"`
}

type Expectation struct {
	ClientID, ApplicationCode, EnvironmentCode string
	RoleConfigHash                             string
}

type ScopeDecision struct {
	AllowAll        bool
	OrganizationIDs []string
	SelfIDs         []string
	ProjectIDs      []string
}

// Validate performs the fail-closed structural and application-boundary checks
// shared by CRM and Portal. It intentionally does not derive permissions from
// roles: the platform response is the effective authorization result, while a
// subsystem catalog is only an upper-bound allow-list.
func Validate(response Response, expected Expectation) ([]sharedauth.DataScope, ScopeDecision, error) {
	if response.Subject == "" || response.Subject != strings.TrimSpace(response.Subject) ||
		response.IdentityID == "" || response.IdentityID != strings.TrimSpace(response.IdentityID) || response.TenantID == "" ||
		response.TenantID != strings.TrimSpace(response.TenantID) || response.AuthorizationRevision == 0 {
		return nil, ScopeDecision{}, errors.New("authorization context identity or revision is invalid")
	}
	canonicalSubjectID := strings.TrimSpace(response.SubjectID)
	if canonicalSubjectID == "" {
		// 兼容尚未发布 subject_id 的 N-1 平台。
		canonicalSubjectID = response.IdentityID
	}
	if canonicalSubjectID != response.IdentityID {
		return nil, ScopeDecision{}, errors.New("authorization context subject_id and identity_id do not match")
	}
	if strings.TrimSpace(expected.ClientID) == "" || strings.TrimSpace(expected.ApplicationCode) == "" || strings.TrimSpace(expected.EnvironmentCode) == "" {
		return nil, ScopeDecision{}, errors.New("authorization context expectation is incomplete")
	}
	if response.ClientID != expected.ClientID || response.ApplicationCode != expected.ApplicationCode || response.EnvironmentCode != expected.EnvironmentCode {
		return nil, ScopeDecision{}, errors.New("authorization context application boundary does not match the current subsystem")
	}
	if err := validateRoleConfigCompatibility(response, expected.RoleConfigHash); err != nil {
		return nil, ScopeDecision{}, err
	}
	canonical, decision, err := ValidateScopes(response.DataScopes, response.Roles, expected.EnvironmentCode, canonicalSubjectID, response.PersonID)
	if err != nil {
		return nil, ScopeDecision{}, err
	}
	return canonical, decision, nil
}

func validateRoleConfigCompatibility(response Response, expectedHash string) error {
	expectedHash = strings.TrimSpace(expectedHash)
	allMissing := strings.TrimSpace(response.CatalogVersion) == "" && len(response.CompatibleCatalogVersions) == 0 &&
		strings.TrimSpace(response.RoleConfigHash) == "" && len(response.CompatibleRoleConfigHashes) == 0
	if allMissing {
		// N-1 平台尚未返回兼容窗口，滚动升级期间沿用既有本地目录校验。
		return nil
	}
	if expectedHash == "" || response.CatalogVersion == "" || response.RoleConfigHash == "" ||
		len(response.CompatibleCatalogVersions) == 0 || len(response.CompatibleCatalogVersions) > 2 ||
		len(response.CompatibleRoleConfigHashes) == 0 || len(response.CompatibleRoleConfigHashes) > 2 {
		return errors.New("authorization catalog compatibility window is incomplete")
	}
	if !containsCanonical(response.CompatibleCatalogVersions, response.CatalogVersion) ||
		!containsCanonical(response.CompatibleRoleConfigHashes, response.RoleConfigHash) ||
		!containsCanonical(response.CompatibleRoleConfigHashes, expectedHash) {
		return errors.New("local role configuration is outside the N/N-1 compatibility window")
	}
	return nil
}

func containsCanonical(values []string, expected string) bool {
	found := false
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
		found = found || value == expected
	}
	return found
}

// ValidateScopes can be reused at the service boundary after an adapter has
// decoded a response. Keeping this second validation prevents a test double or
// alternate adapter from bypassing the same data-scope rules.
func ValidateScopes(input []sharedauth.DataScope, inputRoles []string, environmentCode, identityID, personID string) ([]sharedauth.DataScope, ScopeDecision, error) {
	if strings.TrimSpace(environmentCode) == "" || environmentCode != strings.TrimSpace(environmentCode) {
		return nil, ScopeDecision{}, errors.New("authorization context environment expectation is invalid")
	}
	if strings.TrimSpace(identityID) == "" || identityID != strings.TrimSpace(identityID) || personID != strings.TrimSpace(personID) {
		return nil, ScopeDecision{}, errors.New("authorization context scope identity is invalid")
	}
	roleSet, err := canonicalSet(inputRoles, 64, 128)
	if err != nil || len(roleSet) == 0 {
		return nil, ScopeDecision{}, fmt.Errorf("authorization context roles are invalid: %w", err)
	}
	roles := make(map[string]struct{}, len(roleSet))
	for _, role := range roleSet {
		roles[role] = struct{}{}
	}

	canonical := make([]sharedauth.DataScope, 0, len(input))
	decision := ScopeDecision{}
	seen := make(map[string]struct{}, len(input))
	for _, raw := range input {
		scope := sharedauth.DataScope{
			RoleCode: strings.TrimSpace(raw.RoleCode), ScopeType: strings.TrimSpace(raw.ScopeType),
			ScopeID: strings.TrimSpace(raw.ScopeID), EnvironmentCode: strings.TrimSpace(raw.EnvironmentCode),
		}
		if scope != raw || scope.RoleCode == "" || len(scope.RoleCode) > 128 || len(scope.ScopeType) > 32 || len(scope.ScopeID) > 128 || len(scope.EnvironmentCode) > 64 {
			return nil, ScopeDecision{}, errors.New("authorization context data scope is not canonical")
		}
		if _, known := roles[scope.RoleCode]; !known {
			return nil, ScopeDecision{}, errors.New("authorization context data scope references an unknown role")
		}
		switch scope.ScopeType {
		case ScopeApplication, ScopeTenant:
			// The platform exposes historical TENANT role bindings as APPLICATION.
			// Both represent all data in the current application and carry no
			// business scope identifier or environment code.
			if scope.ScopeID != "" || scope.EnvironmentCode != "" {
				return nil, ScopeDecision{}, errors.New("application data scope must not carry scope_id or environment_code")
			}
			decision.AllowAll = true
		case ScopeEnvironment:
			if scope.ScopeID == "" || scope.EnvironmentCode != environmentCode {
				return nil, ScopeDecision{}, errors.New("environment data scope does not match the current environment")
			}
			decision.AllowAll = true
		case ScopeOrg, ScopeSelf, ScopeProject:
			if scope.ScopeID == "" || scope.EnvironmentCode != environmentCode {
				return nil, ScopeDecision{}, errors.New("fine-grained data scope is incomplete or belongs to another environment")
			}
			switch scope.ScopeType {
			case ScopeOrg:
				decision.OrganizationIDs = append(decision.OrganizationIDs, scope.ScopeID)
			case ScopeSelf:
				if scope.ScopeID != identityID && (personID == "" || scope.ScopeID != personID) {
					return nil, ScopeDecision{}, errors.New("self data scope belongs to another identity")
				}
				decision.SelfIDs = append(decision.SelfIDs, scope.ScopeID)
			case ScopeProject:
				decision.ProjectIDs = append(decision.ProjectIDs, scope.ScopeID)
			}
		default:
			return nil, ScopeDecision{}, errors.New("authorization context data scope type is unknown")
		}
		key := scope.RoleCode + "\x00" + scope.ScopeType + "\x00" + scope.ScopeID + "\x00" + scope.EnvironmentCode
		if _, duplicate := seen[key]; duplicate {
			return nil, ScopeDecision{}, errors.New("authorization context contains duplicate data scopes")
		}
		seen[key] = struct{}{}
		canonical = append(canonical, scope)
	}
	if len(canonical) == 0 {
		return nil, ScopeDecision{}, errors.New("authorization context has no applicable data scope")
	}
	sortScopes(canonical)
	decision.OrganizationIDs = sortedUnique(decision.OrganizationIDs)
	decision.SelfIDs = sortedUnique(decision.SelfIDs)
	decision.ProjectIDs = sortedUnique(decision.ProjectIDs)
	return canonical, decision, nil
}

func canonicalSet(values []string, maxItems, maxBytes int) ([]string, error) {
	if len(values) > maxItems {
		return nil, errors.New("too many values")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > maxBytes || value == "all" {
			return nil, errors.New("value is empty, non-canonical, overlong, or a wildcard")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("duplicate value")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func sortedUnique(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func sortScopes(values []sharedauth.DataScope) {
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i], values[j]
		if left.RoleCode != right.RoleCode {
			return left.RoleCode < right.RoleCode
		}
		if left.ScopeType != right.ScopeType {
			return left.ScopeType < right.ScopeType
		}
		if left.ScopeID != right.ScopeID {
			return left.ScopeID < right.ScopeID
		}
		return left.EnvironmentCode < right.EnvironmentCode
	})
}
