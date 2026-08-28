package crmauth

import (
	"testing"
	"time"
)

func TestValidateLogoutTokenEnforcesCRMProtocolBoundary(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	valid := verifiedLogoutToken{Header: map[string]any{"typ": "logout+jwt"}, Claims: map[string]any{
		"iss": "https://issuer.example/realm", "aud": "crm-prod-web", "jti": "jti-1", "sid": "sid-1",
		"iat": float64(now.Add(-time.Second).Unix()), "exp": float64(now.Add(time.Minute).Unix()),
		"events": map[string]any{logoutEventURI: map[string]any{}},
	}}
	if _, err := validateLogoutToken(valid, "https://issuer.example/realm", "crm-prod-web", now, 5*time.Minute); err != nil {
		t.Fatalf("valid logout token rejected: %v", err)
	}
	tests := map[string]func(map[string]any){
		"wrong audience":    func(c map[string]any) { c["aud"] = "portal-prod-web" },
		"nonce present":     func(c map[string]any) { c["nonce"] = "" },
		"no sid or sub":     func(c map[string]any) { delete(c, "sid") },
		"lifetime too long": func(c map[string]any) { c["exp"] = float64(now.Add(10 * time.Minute).Unix()) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			claims := map[string]any{}
			for k, v := range valid.Claims {
				claims[k] = v
			}
			mutate(claims)
			candidate := valid
			candidate.Claims = claims
			if _, err := validateLogoutToken(candidate, "https://issuer.example/realm", "crm-prod-web", now, 5*time.Minute); err == nil {
				t.Fatal("invalid logout token accepted")
			}
		})
	}
}
