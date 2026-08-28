package portalbootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
)

const portalLogoutEventURI = "http://schemas.openid.net/event/backchannel-logout"

type portalVerifiedLogoutToken struct{ Header, Claims map[string]any }
type portalBackchannelVerifier interface {
	VerifyPortalBackchannelLogout(context.Context, string) (portalVerifiedLogoutToken, error)
}

// VerifyPortalBackchannelLogout 使用 Portal Client 自己的 audience 和 JWKS 校验注销令牌。
func (a *OIDCAdapter) VerifyPortalBackchannelLogout(ctx context.Context, raw string) (portalVerifiedLogoutToken, error) {
	if a == nil || a.verifier == nil {
		return portalVerifiedLogoutToken{}, errors.New("Portal logout verifier is unavailable")
	}
	idToken, err := a.verifier.Verify(oidc.ClientContext(ctx, a.httpClient), raw)
	if err != nil {
		return portalVerifiedLogoutToken{}, err
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return portalVerifiedLogoutToken{}, errors.New("logout token is malformed")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return portalVerifiedLogoutToken{}, errors.New("logout token header is malformed")
	}
	var header, claims map[string]any
	if json.Unmarshal(headerBytes, &header) != nil || idToken.Claims(&claims) != nil {
		return portalVerifiedLogoutToken{}, errors.New("logout token claims are malformed")
	}
	return portalVerifiedLogoutToken{Header: header, Claims: claims}, nil
}

type portalBackchannelLogoutHandler struct {
	verifier         portalBackchannelVerifier
	repo             *account.GORMRepository
	issuer, clientID string
	maxTTL           time.Duration
	now              func() time.Time
}

func newPortalBackchannelLogoutHandler(verifier portalBackchannelVerifier, repo *account.GORMRepository, issuer, clientID string, maxTTL time.Duration) *portalBackchannelLogoutHandler {
	return &portalBackchannelLogoutHandler{verifier: verifier, repo: repo, issuer: strings.TrimSpace(issuer), clientID: strings.TrimSpace(clientID), maxTTL: maxTTL, now: time.Now}
}

// Handle 只接受标准 logout_token 表单，并对合法重放保持 204 幂等响应。
func (h *portalBackchannelLogoutHandler) Handle(c *gin.Context) {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		c.Status(http.StatusBadRequest)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 20<<10)
	if c.Request.ParseForm() != nil || len(c.Request.PostForm) != 1 || len(c.Request.PostForm["logout_token"]) != 1 {
		c.Status(http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(c.Request.PostForm.Get("logout_token"))
	if raw == "" || len(raw) > 16<<10 {
		c.Status(http.StatusBadRequest)
		return
	}
	token, err := h.verifier.VerifyPortalBackchannelLogout(c.Request.Context(), raw)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	claims, err := validatePortalLogoutToken(token, h.issuer, h.clientID, h.now().UTC(), h.maxTTL)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	if _, err = h.repo.ApplyBackchannelLogout(c.Request.Context(), claims.jti, claims.issuer, claims.subject, claims.sid, claims.expiresAt, h.now().UTC()); err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

type portalLogoutClaims struct {
	issuer, subject, sid, jti string
	expiresAt                 time.Time
}

func validatePortalLogoutToken(token portalVerifiedLogoutToken, issuer, clientID string, now time.Time, maxTTL time.Duration) (portalLogoutClaims, error) {
	if maxTTL <= 0 {
		maxTTL = 5 * time.Minute
	}
	if token.Header["typ"] != "logout+jwt" {
		return portalLogoutClaims{}, errors.New("invalid typ")
	}
	text := func(name string) string { value, _ := token.Claims[name].(string); return strings.TrimSpace(value) }
	iss, jti, sub, sid := text("iss"), text("jti"), text("sub"), text("sid")
	if iss != issuer || jti == "" || len(jti) > 128 || (sub == "" && sid == "") || len(sub) > 128 || len(sid) > 128 {
		return portalLogoutClaims{}, errors.New("invalid identity claims")
	}
	if _, exists := token.Claims["nonce"]; exists {
		return portalLogoutClaims{}, errors.New("nonce is forbidden")
	}
	if !portalAudienceContains(token.Claims["aud"], clientID) {
		return portalLogoutClaims{}, errors.New("invalid audience")
	}
	events, ok := token.Claims["events"].(map[string]any)
	event, exists := events[portalLogoutEventURI]
	object, objectOK := event.(map[string]any)
	if !ok || !exists || !objectOK || len(object) != 0 {
		return portalLogoutClaims{}, errors.New("invalid event")
	}
	iat, iatOK := portalNumericDate(token.Claims["iat"])
	exp, expOK := portalNumericDate(token.Claims["exp"])
	if !iatOK || !expOK || exp <= iat || time.Duration(exp-iat)*time.Second > maxTTL || now.Before(time.Unix(iat, 0).Add(-30*time.Second)) || !now.Before(time.Unix(exp, 0)) {
		return portalLogoutClaims{}, errors.New("invalid lifetime")
	}
	return portalLogoutClaims{issuer: iss, subject: sub, sid: sid, jti: jti, expiresAt: time.Unix(exp, 0).UTC()}, nil
}

func portalAudienceContains(value any, expected string) bool {
	switch typed := value.(type) {
	case string:
		return typed == expected
	case []any:
		for _, entry := range typed {
			if text, ok := entry.(string); ok && text == expected {
				return true
			}
		}
	}
	return false
}
func portalNumericDate(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}
