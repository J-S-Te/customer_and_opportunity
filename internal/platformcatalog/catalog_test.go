package platformcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestApplicationManifestsAreCompleteAndIndependent(t *testing.T) {
	crm := CRMManifest()
	portal := PortalManifest()
	for name, manifest := range map[string]Manifest{"crm": crm, "portal": portal} {
		if _, err := normalizeManifest(manifest); err != nil {
			t.Fatalf("%s manifest rejected: %v", name, err)
		}
	}
	for _, expected := range []string{"customer.read", "customer.duplicate.override", "opportunity.stage.change", "opportunity.contract.transfer", "opportunity.attachment.read", "opportunity.attachment.upload", "opportunity.attachment.download", "presale.worklog", "presale.contact_phone.read"} {
		if !HasPermission(crm, expected) {
			t.Fatalf("CRM manifest is missing %s", expected)
		}
	}
	for _, roleCode := range []string{"sales_director", "customer_admin"} {
		if !roleHasPermission(crm, roleCode, "customer.duplicate.override") {
			t.Fatalf("CRM role %s cannot authorize the duplicate override workflow", roleCode)
		}
	}
	for _, roleCode := range []string{"sales_director", "crm_super_admin"} {
		if !roleHasPermission(crm, roleCode, "customer.credit.approve") {
			t.Fatalf("CRM role %s cannot authorize the credit approval workflow", roleCode)
		}
	}
	if roleHasPermission(crm, "sales", "customer.duplicate.override") {
		t.Fatal("ordinary sales role received the high-risk duplicate override permission")
	}
	if !roleHasPermission(crm, "sales_director", "opportunity.contract.transfer") || roleHasPermission(crm, "sales", "opportunity.contract.transfer") {
		t.Fatal("high-risk contract transfer permission is not limited to the sales director role")
	}
	for _, roleCode := range []string{"sales_director", "technical_director", "team_lead"} {
		if !roleHasPermission(crm, roleCode, "presale.contact_phone.read") {
			t.Fatalf("CRM role %s cannot enter the separately authorized contact-phone workflow", roleCode)
		}
		if !roleHasPermission(crm, roleCode, "customer.void") || !roleHasPermission(crm, roleCode, "customer.restore") {
			t.Fatalf("CRM role %s cannot void and restore customers", roleCode)
		}
	}
	for _, retired := range []string{"implementation_engineer", "technical_lead"} {
		if HasRole(crm, retired) {
			t.Fatalf("retired CRM role %s is still exposed", retired)
		}
	}
	if roleHasPermission(crm, "technician", "presale.contact_phone.read") {
		t.Fatal("technician received plaintext presale contact-phone permission")
	}
	for _, forbidden := range []string{"customer.read", "opportunity.read", "presale.create", "presale.approve", "presale.assign", "presale.report", "presale.alert.config"} {
		if roleHasPermission(crm, "technician", forbidden) {
			t.Fatalf("technician received non-execution permission %s", forbidden)
		}
	}
	for _, permission := range []string{"presale.approve", "presale.assign"} {
		if !roleHasPermission(crm, "technical_director", permission) {
			t.Fatalf("technical director cannot execute configured approval workflow permission %s", permission)
		}
	}
	for _, roleCode := range []string{"sales", "auditor", "customer_admin"} {
		if roleHasPermission(crm, roleCode, "presale.contact_phone.read") {
			t.Fatalf("CRM role %s received plaintext presale contact-phone permission", roleCode)
		}
	}
	if HasPermission(crm, "report.download") || HasRole(crm, "portal_customer") {
		t.Fatal("CRM manifest accepted Portal browser authorization")
	}
	for _, expected := range []string{"project.read", "report.download", "filing.submit", "feedback.reply", "account.security.manage"} {
		if !HasPermission(portal, expected) {
			t.Fatalf("Portal manifest is missing %s", expected)
		}
	}
	if HasPermission(portal, "customer.read") || !HasRole(portal, "portal_customer") {
		t.Fatal("Portal manifest is not isolated from CRM authorization")
	}
	for _, machineOnly := range []string{"authorization.catalog.sync", "portal.feedback.manage", "portal.report.risk.manage", "report.callback.write", "portal.identity_mapping.provision"} {
		if HasPermission(crm, machineOnly) || HasPermission(portal, machineOnly) {
			t.Fatalf("machine-only scope %s leaked into a browser role catalog", machineOnly)
		}
	}
}

func roleHasPermission(manifest Manifest, roleCode, permissionCode string) bool {
	for _, role := range manifest.Roles {
		if role.Code != roleCode {
			continue
		}
		for _, permission := range role.Permissions {
			if permission == permissionCode {
				return true
			}
		}
	}
	return false
}

func TestClaimsRoleConfigHashIsDeterministicAndMappingBound(t *testing.T) {
	manifest := CRMManifest()
	first, err := ClaimsRoleConfigHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Permissions[0], manifest.Permissions[1] = manifest.Permissions[1], manifest.Permissions[0]
	manifest.Roles[0].Permissions = append([]string{"customer.update"}, manifest.Roles[0].Permissions...)
	second, err := ClaimsRoleConfigHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("presentation ordering changed Claims hash: %s != %s", first, second)
	}
	checksum, err := CatalogChecksum(CRMManifest())
	if err != nil || !strings.HasPrefix(checksum, "sha256:") || len(checksum) != len("sha256:")+64 {
		t.Fatalf("catalog checksum = %q, %v", checksum, err)
	}
	manifest.Roles[0].Name = "仅修改展示名称"
	presentationChanged, err := ClaimsRoleConfigHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if presentationChanged != first {
		t.Fatal("role presentation metadata changed Claims hash")
	}
	manifest.Roles[0].Permissions = append(manifest.Roles[0].Permissions, "customer.import")
	changed, err := ClaimsRoleConfigHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("role-permission mapping change did not change Claims hash")
	}
	if err := ValidateClaimsRoleConfigHash(CRMManifest(), "wrong"); err == nil {
		t.Fatal("mismatched runtime Claims hash was accepted")
	}
}

func TestPublishUsesApplicationBoundPublisherContract(t *testing.T) {
	step := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		step++
		switch step {
		case 1:
			if request.Method != http.MethodPost || request.URL.String() != "https://identity.example/oauth2/token" {
				t.Fatalf("token request = %s %s", request.Method, request.URL)
			}
			clientID, secret, ok := request.BasicAuth()
			body, _ := io.ReadAll(request.Body)
			if !ok || clientID != "publisher" || secret != "secret" || string(body) != "grant_type=client_credentials&scope=authorization.catalog.sync" {
				t.Fatalf("unexpected token contract: client=%q body=%q", clientID, body)
			}
			return jsonResponse(http.StatusOK, `{"access_token":"token","token_type":"Bearer","scope":"authorization.catalog.sync"}`), nil
		case 2:
			if request.Method != http.MethodPut || request.URL.String() != "https://identity.example/api/v1/applications/app-1/authorization-catalog" || request.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("publish request = %s %s authorization=%q", request.Method, request.URL, request.Header.Get("Authorization"))
			}
			var payload catalogPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.CatalogVersion == "" || payload.Checksum == "" || payload.ClaimsRoleConfigHash == "" || len(payload.Permissions) == 0 || len(payload.Roles) == 0 || payload.Policy.MaxEffectiveRoles != 1 {
				t.Fatalf("published payload = %#v", payload)
			}
			return jsonResponse(http.StatusOK, `{}`), nil
		default:
			t.Fatalf("unexpected request %s", request.URL)
			return nil, errors.New("unexpected request")
		}
	})}
	err := Publish(context.Background(), PortalManifest(), Options{
		Enabled: true, BaseURL: "https://identity.example", ApplicationID: "app-1",
		ClientID: "publisher", ClientSecret: "secret", HTTPClient: client,
	})
	if err != nil || step != 2 {
		t.Fatalf("Publish() step=%d error=%v", step, err)
	}
}

func TestPublishRejectsRedirectsInsteadOfForwardingCredentials(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://evil.example/token"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	err := Publish(context.Background(), CRMManifest(), Options{
		Enabled: true, BaseURL: "https://identity.example", ApplicationID: "app-1",
		ClientID: "publisher", ClientSecret: "secret", HTTPClient: client,
	})
	if err == nil {
		t.Fatal("OAuth redirect was followed or accepted")
	}
}

func TestPublishFailsClosedWithoutLeakingResponseOrSecrets(t *testing.T) {
	if err := Publish(context.Background(), CRMManifest(), Options{Enabled: true, BaseURL: "https://user:password@identity.example"}); err == nil || strings.Contains(err.Error(), "password") {
		t.Fatalf("invalid options error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"access_token":"do-not-log"}`), nil
	})}
	err := Publish(context.Background(), CRMManifest(), Options{Enabled: true, BaseURL: "https://identity.example", ApplicationID: "app-1", ClientID: "publisher", ClientSecret: "secret", HTTPClient: client})
	if err == nil || strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe publication error = %v", err)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
