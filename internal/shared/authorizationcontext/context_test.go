package authorizationcontext

import (
	"testing"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func validResponse() Response {
	return Response{
		Subject: "identity-a", IdentityID: "identity-a", TenantID: "tenant-a",
		ClientID: "crm-dev-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev",
		Roles: []string{"sales"}, Permissions: []string{"customer.read"}, AuthorizationRevision: 3,
		DataScopes: []sharedauth.DataScope{{RoleCode: "sales", ScopeType: ScopeApplication}},
	}
}

func TestValidateInterpretsApplicationTenantAndEnvironmentAsAllowAll(t *testing.T) {
	expected := Expectation{ClientID: "crm-dev-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}
	for _, scope := range []sharedauth.DataScope{
		{RoleCode: "sales", ScopeType: ScopeApplication},
		{RoleCode: "sales", ScopeType: ScopeTenant},
		{RoleCode: "sales", ScopeType: ScopeEnvironment, ScopeID: "01ENV", EnvironmentCode: "dev"},
	} {
		response := validResponse()
		response.DataScopes = []sharedauth.DataScope{scope}
		_, decision, err := Validate(response, expected)
		if err != nil || !decision.AllowAll {
			t.Fatalf("scope=%#v decision=%#v err=%v", scope, decision, err)
		}
	}
}

func TestValidateRejectsMalformedOrCrossApplicationScopes(t *testing.T) {
	expected := Expectation{ClientID: "crm-dev-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}
	tests := []func(*Response){
		func(value *Response) { value.SubjectID = "identity-b" },
		func(value *Response) { value.ClientID = "portal-dev-web" },
		func(value *Response) { value.ApplicationCode = "customer_portal" },
		func(value *Response) { value.EnvironmentCode = "prod" },
		func(value *Response) { value.DataScopes[0].ScopeID = "must-be-empty" },
		func(value *Response) {
			value.DataScopes = []sharedauth.DataScope{{RoleCode: "sales", ScopeType: ScopeEnvironment, ScopeID: "01ENV", EnvironmentCode: "prod"}}
		},
		func(value *Response) {
			value.DataScopes = []sharedauth.DataScope{{RoleCode: "sales", ScopeType: ScopeOrg, ScopeID: "", EnvironmentCode: "dev"}}
		},
		func(value *Response) {
			value.DataScopes = []sharedauth.DataScope{{RoleCode: "sales", ScopeType: ScopeSelf, ScopeID: "identity-b", EnvironmentCode: "dev"}}
		},
		func(value *Response) {
			value.DataScopes = []sharedauth.DataScope{{RoleCode: "sales", ScopeType: "CUSTOMER", ScopeID: "7", EnvironmentCode: "dev"}}
		},
		func(value *Response) { value.DataScopes = append(value.DataScopes, value.DataScopes[0]) },
	}
	for index, mutate := range tests {
		response := validResponse()
		mutate(&response)
		if _, _, err := Validate(response, expected); err == nil {
			t.Fatalf("case %d unexpectedly passed: %#v", index, response)
		}
	}
}

func TestValidateAllowsSelfScopeOnlyForIdentityOrPerson(t *testing.T) {
	response := validResponse()
	response.PersonID = "person-a"
	expected := Expectation{ClientID: "crm-dev-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}
	for _, scopeID := range []string{"identity-a", "person-a"} {
		response.DataScopes = []sharedauth.DataScope{{RoleCode: "sales", ScopeType: ScopeSelf, ScopeID: scopeID, EnvironmentCode: "dev"}}
		if _, _, err := Validate(response, expected); err != nil {
			t.Fatalf("SELF scope %q rejected: %v", scopeID, err)
		}
	}
}

func TestValidateRetainsFineGrainedScopes(t *testing.T) {
	response := validResponse()
	response.DataScopes = []sharedauth.DataScope{
		{RoleCode: "sales", ScopeType: ScopeOrg, ScopeID: "org-b", EnvironmentCode: "dev"},
		{RoleCode: "sales", ScopeType: ScopeSelf, ScopeID: "identity-a", EnvironmentCode: "dev"},
		{RoleCode: "sales", ScopeType: ScopeProject, ScopeID: "project-a", EnvironmentCode: "dev"},
	}
	_, decision, err := Validate(response, Expectation{ClientID: "crm-dev-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"})
	if err != nil || decision.AllowAll || len(decision.OrganizationIDs) != 1 || len(decision.SelfIDs) != 1 || len(decision.ProjectIDs) != 1 {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}
