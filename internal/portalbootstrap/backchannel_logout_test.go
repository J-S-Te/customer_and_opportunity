package portalbootstrap

import (
	"testing"
	"time"
)

func TestValidatePortalLogoutTokenUsesPortalAudience(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	token := portalVerifiedLogoutToken{Header: map[string]any{"typ": "logout+jwt"}, Claims: map[string]any{
		"iss": "https://issuer.example/realm", "aud": "portal-prod-web", "jti": "portal-jti", "sub": "portal-subject",
		"iat": float64(now.Add(-time.Second).Unix()), "exp": float64(now.Add(time.Minute).Unix()),
		"events": map[string]any{portalLogoutEventURI: map[string]any{}},
	}}
	if _, err := validatePortalLogoutToken(token, "https://issuer.example/realm", "portal-prod-web", now, 5*time.Minute); err != nil {
		t.Fatalf("valid Portal logout rejected: %v", err)
	}
	if _, err := validatePortalLogoutToken(token, "https://issuer.example/realm", "crm-prod-web", now, 5*time.Minute); err == nil {
		t.Fatal("CRM audience was accepted by Portal receiver")
	}
	token.Claims["nonce"] = "forbidden"
	if _, err := validatePortalLogoutToken(token, "https://issuer.example/realm", "portal-prod-web", now, 5*time.Minute); err == nil {
		t.Fatal("logout token containing nonce was accepted")
	}
}
