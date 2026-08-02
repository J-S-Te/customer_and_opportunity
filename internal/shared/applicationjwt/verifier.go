// Package applicationjwt verifies the base platform's client-credentials JWTs.
// It is intentionally independent from OIDC Discovery: the platform's OIDC JWKS
// publishes a different signing manager and cannot verify application tokens.
package applicationjwt

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const allowedClockSkew = time.Minute

// Claims is the complete signed application identity issued by the base platform.
type Claims struct {
	Subject         string
	OAuthClientID   string
	TenantID        string
	ApplicationID   string
	ApplicationCode string
	EnvironmentID   string
	EnvironmentCode string
	Scopes          []string
	IssuedAt        time.Time
	NotBefore       time.Time
	ExpiresAt       time.Time
}

// Verifier accepts only the platform's EdDSA application-token contract and a
// public key. A subsystem must never receive the platform signing private key.
type Verifier struct {
	issuer    string
	audience  string
	publicKey ed25519.PublicKey
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

type jwtPayload struct {
	Issuer          string   `json:"iss"`
	Audience        string   `json:"aud"`
	TokenUse        string   `json:"token_use"`
	Subject         string   `json:"sub"`
	OAuthClientID   string   `json:"oauth_client_id"`
	TenantID        string   `json:"tenant_id"`
	ApplicationID   string   `json:"application_id"`
	ApplicationCode string   `json:"application_code"`
	EnvironmentID   string   `json:"environment_id"`
	EnvironmentCode string   `json:"environment_code"`
	Scopes          []string `json:"scope"`
	IssuedAt        int64    `json:"iat"`
	NotBefore       int64    `json:"nbf"`
	ExpiresAt       int64    `json:"exp"`
}

// LoadVerifier loads one PKIX Ed25519 public key from a read-only PEM file.
func LoadVerifier(issuer, audience, publicKeyPath string) (*Verifier, error) {
	if strings.TrimSpace(issuer) == "" || issuer != strings.TrimSpace(issuer) {
		return nil, errors.New("application JWT issuer must not be empty or contain surrounding whitespace")
	}
	if strings.TrimSpace(audience) == "" || audience != strings.TrimSpace(audience) {
		return nil, errors.New("application JWT audience must not be empty or contain surrounding whitespace")
	}
	publicKey, err := loadPublicKey(publicKeyPath)
	if err != nil {
		return nil, err
	}
	return &Verifier{issuer: issuer, audience: audience, publicKey: publicKey}, nil
}

// Verify validates signature, header, issuer, audience, token use, timestamps,
// tenant/application/environment binding and the canonical scope set.
func (v *Verifier) Verify(rawToken string, now time.Time) (Claims, error) {
	if v == nil {
		return Claims{}, errors.New("application JWT verifier must not be nil")
	}
	if now.IsZero() {
		return Claims{}, errors.New("application JWT validation time must not be zero")
	}
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Claims{}, errors.New("application JWT has an invalid compact serialization")
	}
	var header jwtHeader
	if err := decodeJSON(parts[0], &header); err != nil {
		return Claims{}, fmt.Errorf("decode application JWT header: %w", err)
	}
	if header.Algorithm != "EdDSA" || header.Type != "JWT" {
		return Claims{}, errors.New("application JWT uses an unsupported header")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(v.publicKey, []byte(parts[0]+"."+parts[1]), signature) {
		return Claims{}, errors.New("application JWT signature verification failed")
	}
	var payload jwtPayload
	if err := decodeJSON(parts[1], &payload); err != nil {
		return Claims{}, fmt.Errorf("decode application JWT payload: %w", err)
	}
	if payload.Issuer != v.issuer || payload.Audience != v.audience || payload.TokenUse != "application" {
		return Claims{}, errors.New("application JWT issuer, audience or token use does not match")
	}
	if payload.IssuedAt <= 0 || payload.NotBefore <= 0 || payload.ExpiresAt <= 0 {
		return Claims{}, errors.New("application JWT temporal claims are missing")
	}
	claims := Claims{
		Subject: payload.Subject, OAuthClientID: payload.OAuthClientID, TenantID: payload.TenantID,
		ApplicationID: payload.ApplicationID, ApplicationCode: payload.ApplicationCode,
		EnvironmentID: payload.EnvironmentID, EnvironmentCode: payload.EnvironmentCode,
		Scopes: append([]string(nil), payload.Scopes...), IssuedAt: time.Unix(payload.IssuedAt, 0).UTC(),
		NotBefore: time.Unix(payload.NotBefore, 0).UTC(), ExpiresAt: time.Unix(payload.ExpiresAt, 0).UTC(),
	}
	if err := validateClaims(claims); err != nil {
		return Claims{}, err
	}
	now = now.UTC()
	if !claims.ExpiresAt.After(now) || claims.IssuedAt.After(now.Add(allowedClockSkew)) || claims.NotBefore.After(now.Add(allowedClockSkew)) {
		return Claims{}, errors.New("application JWT is expired or not yet valid")
	}
	return claims, nil
}

func validateClaims(claims Claims) error {
	for _, value := range []string{claims.Subject, claims.OAuthClientID, claims.TenantID, claims.ApplicationID, claims.ApplicationCode, claims.EnvironmentID, claims.EnvironmentCode} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("application JWT contains an invalid required claim")
		}
	}
	if len(claims.Scopes) == 0 || claims.NotBefore.Before(claims.IssuedAt) || !claims.ExpiresAt.After(claims.NotBefore) {
		return errors.New("application JWT contains invalid scopes or timestamps")
	}
	seen := make(map[string]struct{}, len(claims.Scopes))
	for _, scope := range claims.Scopes {
		if scope == "" || scope != strings.TrimSpace(scope) {
			return errors.New("application JWT contains an invalid scope")
		}
		if _, duplicate := seen[scope]; duplicate {
			return errors.New("application JWT contains duplicated scopes")
		}
		seen[scope] = struct{}{}
	}
	return nil
}

func loadPublicKey(path string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("application JWT public key path must not be empty")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read application JWT public key: %w", err)
	}
	block, remainder := pem.Decode(contents)
	if block == nil || len(bytes.TrimSpace(remainder)) != 0 {
		return nil, errors.New("application JWT public key must contain exactly one PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse application JWT public key: %w", err)
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("application JWT public key must be an Ed25519 PKIX key")
	}
	return append(ed25519.PublicKey(nil), publicKey...), nil
}

func decodeJSON(encoded string, destination any) error {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return err
	}
	return nil
}
