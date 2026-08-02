package applicationjwt

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifierMatchesPlatformApplicationTokenContract(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := writePublicKey(t, publicKey)
	verifier, err := LoadVerifier("basic-platform", "basic-platform-application", path)
	if err != nil {
		t.Fatalf("LoadVerifier() error = %v", err)
	}
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	token := signToken(t, privateKey, jwtPayload{
		Issuer: "basic-platform", Audience: "basic-platform-application", TokenUse: "application",
		Subject: "crm-client", OAuthClientID: "01J00000000000000000000001", TenantID: "01J00000000000000000000000",
		ApplicationID: "01J00000000000000000000002", ApplicationCode: "crm", EnvironmentID: "01J00000000000000000000003",
		EnvironmentCode: "prod", Scopes: []string{"portal.invite.verify"}, IssuedAt: now.Add(-time.Minute).Unix(),
		NotBefore: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(10 * time.Minute).Unix(),
	})
	claims, err := verifier.Verify(token, now)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != "crm-client" || claims.OAuthClientID != "01J00000000000000000000001" || claims.Scopes[0] != "portal.invite.verify" {
		t.Fatalf("Verify() claims = %#v", claims)
	}
}

func TestVerifierRejectsOIDCOrMalformedApplicationTokens(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, err := LoadVerifier("basic-platform", "basic-platform-application", writePublicKey(t, publicKey))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	base := jwtPayload{
		Issuer: "basic-platform", Audience: "basic-platform-application", TokenUse: "application",
		Subject: "client", OAuthClientID: "row", TenantID: "tenant", ApplicationID: "app", ApplicationCode: "crm",
		EnvironmentID: "env", EnvironmentCode: "prod", Scopes: []string{"scope"}, IssuedAt: now.Add(-time.Minute).Unix(),
		NotBefore: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Minute).Unix(),
	}
	for name, mutate := range map[string]func(*jwtPayload){
		"OIDC issuer":     func(value *jwtPayload) { value.Issuer = "https://identity.example.com" },
		"OIDC token use":  func(value *jwtPayload) { value.TokenUse = "id_token" },
		"wrong audience":  func(value *jwtPayload) { value.Audience = "crm-browser" },
		"missing binding": func(value *jwtPayload) { value.ApplicationID = "" },
		"missing nbf":     func(value *jwtPayload) { value.NotBefore = 0 },
		"duplicate scope": func(value *jwtPayload) { value.Scopes = []string{"scope", "scope"} },
	} {
		t.Run(name, func(t *testing.T) {
			payload := base
			mutate(&payload)
			if _, err := verifier.Verify(signToken(t, privateKey, payload), now); err == nil {
				t.Fatal("invalid token was accepted")
			}
		})
	}
	otherPublic, otherPrivate, _ := ed25519.GenerateKey(rand.Reader)
	_ = otherPublic
	if _, err := verifier.Verify(signToken(t, otherPrivate, base), now); err == nil {
		t.Fatal("token signed by another key was accepted")
	}
}

func writePublicKey(t *testing.T, publicKey ed25519.PublicKey) string {
	t.Helper()
	encoded, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "application-jwt-public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded}), 0o400); err != nil {
		t.Fatal(err)
	}
	return path
}

func signToken(t *testing.T, privateKey ed25519.PrivateKey, payload jwtPayload) string {
	t.Helper()
	headerBytes, _ := json.Marshal(jwtHeader{Algorithm: "EdDSA", Type: "JWT"})
	payloadBytes, _ := json.Marshal(payload)
	signingInput := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(signingInput)))
}
