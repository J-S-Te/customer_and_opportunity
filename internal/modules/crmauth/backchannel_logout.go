package crmauth

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
)

const logoutEventURI = "http://schemas.openid.net/event/backchannel-logout"

type verifiedLogoutToken struct {
	Header map[string]any
	Claims map[string]any
}

type backchannelTokenVerifier interface {
	VerifyBackchannelLogout(context.Context, string) (verifiedLogoutToken, error)
}

// VerifyBackchannelLogout 使用 CRM 自己的 OIDC verifier 校验签名、issuer、audience 与 exp，
// 返回的声明只供 CRM 注销协议继续做严格语义校验。
func (c *platformOIDCClient) VerifyBackchannelLogout(ctx context.Context, raw string) (verifiedLogoutToken, error) {
	if c == nil || c.verifier == nil {
		return verifiedLogoutToken{}, errors.New("CRM logout verifier is unavailable")
	}
	idToken, err := c.verifier.Verify(oidc.ClientContext(ctx, c.httpClient), raw)
	if err != nil {
		return verifiedLogoutToken{}, err
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return verifiedLogoutToken{}, errors.New("logout token is malformed")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return verifiedLogoutToken{}, errors.New("logout token header is malformed")
	}
	var header, claims map[string]any
	if err = json.Unmarshal(headerBytes, &header); err != nil {
		return verifiedLogoutToken{}, errors.New("logout token header is malformed")
	}
	if err = idToken.Claims(&claims); err != nil {
		return verifiedLogoutToken{}, errors.New("logout token claims are malformed")
	}
	return verifiedLogoutToken{Header: header, Claims: claims}, nil
}

// BackchannelLogoutHandler 是 CRM 专属的 OIDC 后通道注销接收端。
type BackchannelLogoutHandler struct {
	verifier backchannelTokenVerifier
	repo     *GORMRepository
	issuer   string
	clientID string
	maxTTL   time.Duration
	now      func() time.Time
}

func NewBackchannelLogoutHandler(verifier backchannelTokenVerifier, repo *GORMRepository, issuer, clientID string, maxTTL time.Duration) *BackchannelLogoutHandler {
	return &BackchannelLogoutHandler{verifier: verifier, repo: repo, issuer: strings.TrimSpace(issuer), clientID: strings.TrimSpace(clientID), maxTTL: maxTTL, now: time.Now}
}

// Handle 校验注销令牌并原子登记 JTI、撤销 CRM 会话；相同 JTI 重放返回 204。
func (h *BackchannelLogoutHandler) Handle(c *gin.Context) {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		c.Status(http.StatusBadRequest)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 20<<10)
	if err = c.Request.ParseForm(); err != nil || len(c.Request.PostForm) != 1 || len(c.Request.PostForm["logout_token"]) != 1 {
		c.Status(http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(c.Request.PostForm.Get("logout_token"))
	if raw == "" || len(raw) > 16<<10 {
		c.Status(http.StatusBadRequest)
		return
	}
	token, err := h.verifier.VerifyBackchannelLogout(c.Request.Context(), raw)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	claims, err := validateLogoutToken(token, h.issuer, h.clientID, h.now().UTC(), h.maxTTL)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	if _, err = h.repo.ApplyBackchannelLogout(c.Request.Context(), claims.JTI, claims.Issuer, claims.Subject, claims.SessionID, claims.ExpiresAt, h.now().UTC()); err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

type logoutClaims struct {
	Issuer, Subject, SessionID, JTI string
	ExpiresAt                       time.Time
}

func validateLogoutToken(token verifiedLogoutToken, issuer, clientID string, now time.Time, maxTTL time.Duration) (logoutClaims, error) {
	if maxTTL <= 0 {
		maxTTL = 5 * time.Minute
	}
	if token.Header["typ"] != "logout+jwt" {
		return logoutClaims{}, errors.New("logout token typ is invalid")
	}
	claimString := func(name string) string { value, _ := token.Claims[name].(string); return strings.TrimSpace(value) }
	iss, jti, sub, sid := claimString("iss"), claimString("jti"), claimString("sub"), claimString("sid")
	if iss != issuer || jti == "" || len(jti) > 128 || (sub == "" && sid == "") || len(sub) > 128 || len(sid) > 128 {
		return logoutClaims{}, errors.New("logout token identity claims are invalid")
	}
	if _, exists := token.Claims["nonce"]; exists {
		return logoutClaims{}, errors.New("logout token must not contain nonce")
	}
	if !audienceContains(token.Claims["aud"], clientID) {
		return logoutClaims{}, errors.New("logout token audience is invalid")
	}
	events, ok := token.Claims["events"].(map[string]any)
	event, exists := events[logoutEventURI]
	if !ok || !exists {
		return logoutClaims{}, errors.New("logout token event is invalid")
	}
	if object, ok := event.(map[string]any); !ok || len(object) != 0 {
		return logoutClaims{}, errors.New("logout token event payload is invalid")
	}
	iat, iatOK := numericDate(token.Claims["iat"])
	exp, expOK := numericDate(token.Claims["exp"])
	if !iatOK || !expOK || exp <= iat || time.Duration(exp-iat)*time.Second > maxTTL || now.Before(time.Unix(iat, 0).Add(-30*time.Second)) || !now.Before(time.Unix(exp, 0)) {
		return logoutClaims{}, errors.New("logout token lifetime is invalid")
	}
	return logoutClaims{Issuer: iss, Subject: sub, SessionID: sid, JTI: jti, ExpiresAt: time.Unix(exp, 0).UTC()}, nil
}

func audienceContains(value any, expected string) bool {
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

func numericDate(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}
