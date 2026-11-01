package crmauth

import (
	"context"
	"errors"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
	"testing"
	"time"
)

type centralTestClient struct {
	state      central.Context
	err        error
	activities int
}

// BeginURL 提供测试桥接地址，实际state仍由被测服务生成。
// 参数：state为原状态，nonce为OIDC秘密。
// 返回值：测试中央入口。
func (c *centralTestClient) BeginURL(state, nonce string) string {
	return "https://platform.test/begin?state=" + state
}

// Complete 提供已验证的测试中央绑定，签名验证由协议用例单独覆盖。
// 参数：请求及票据、访问、身份令牌材料。
// 返回值：服务器绑定及权威周期，或指定错误。
func (c *centralTestClient) Complete(context.Context, string, string, string) (central.Binding, error) {
	return central.Binding{Token: "server-binding", Context: c.state}, c.err
}

// State 模拟只读窗口，不将普通请求计为活动。
// 参数：请求及服务器绑定秘密。
// 返回值：预设期限或依赖错误。
func (c *centralTestClient) State(context.Context, string) (central.Context, error) {
	return c.state, c.err
}

// Activity 统计显式活动，与状态读取区分。
// 参数：请求及服务器绑定秘密。
// 返回值：预设期限或依赖错误。
func (c *centralTestClient) Activity(context.Context, string) (central.Context, error) {
	c.activities++
	return c.state, c.err
}

type centralTestOIDC struct {
	fakeOIDC
	client          *centralTestClient
	refreshes       int
	refreshErr      error
	refreshedExpiry time.Time
}

// ExchangeWithCentral 为业务服务提供已经验签的绑定材料。
// 参数：请求及授权码、PKCE、nonce和中央票据。
// 返回值：原授权及中央绑定；缺票据时拒绝。
func (c *centralTestOIDC) ExchangeWithCentral(ctx context.Context, code, verifier, nonce, ticket string) (verifiedClaims, error) {
	if ticket == "" {
		return verifiedClaims{}, ErrUnauthenticated
	}
	claims, err := c.Exchange(ctx, code, verifier, nonce)
	if err != nil {
		return claims, err
	}
	claims.CentralBinding, err = c.client.Complete(ctx, ticket, claims.AccessToken, "test-id")
	return claims, err
}

// RefreshToken 提供轮换结果，用于验证业务服务的调用时机与持久化。
// 参数：请求及原始刷新令牌。
// 返回值：新材料或明确拒绝、暂不可用错误。
func (c *centralTestOIDC) RefreshToken(context.Context, string) (tokenRefreshResult, error) {
	c.refreshes++
	return tokenRefreshResult{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", ExpiresAt: c.refreshedExpiry}, c.refreshErr
}

type centralTestRepository struct{ *memoryRepo }

// AttachSessionLink 保存测试票据，保持原state与单次消费规则。
// 参数：请求、state摘要、票据密文和服务端时钟。
// 返回值：原事务；到期或重放返回错误。
func (r *centralTestRepository) AttachSessionLink(ctx context.Context, hash string, cipher []byte, now time.Time) (*LoginTransaction, error) {
	if r.login == nil || r.login.StateHash != hash || !r.login.ExpiresAt.After(now) || len(r.login.CentralTicketCipher) > 0 {
		return nil, errNotFound
	}
	r.login.CentralTicketCipher = append([]byte(nil), cipher...)
	return r.login, nil
}

// WithSessionLock 提供业务测试的存储边界；真实数据库并发另行覆盖。
// 参数：请求、Cookie摘要、时钟与持锁操作。
// 返回值：最新投影；确定失效仅撤销当前行。
func (r *centralTestRepository) WithSessionLock(ctx context.Context, hash string, now time.Time, update sessionLockFunc) (*Session, error) {
	session, err := r.FindSession(ctx, hash, now)
	if err != nil {
		return nil, err
	}
	changed, err := update(session)
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, errRefreshRejected) {
		_ = r.RevokeSession(ctx, hash, now)
	}
	if err != nil {
		return nil, err
	}
	if changed {
		r.sessions[hash] = session
	}
	return session, nil
}

// TestShortAccessTokenDoesNotTruncateBrowserSession 验证可信中央绑定才能跨过短令牌窗口。
// 参数：t为测试上下文。
// 返回值：无；短令牌截断会话、刷新续起点或故障误注销时失败。
func TestShortAccessTokenDoesNotTruncateBrowserSession(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	repo := &centralTestRepository{newMemoryRepo()}
	client := &centralTestClient{state: central.Context{SessionID: "canonical-login", TenantID: "tenant-1", SubjectID: "user-1", ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev", PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000, AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds()}}
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(299 * time.Second)
	claims.AccessTokenExpiresAt = claims.ExpiresAt
	claims.RefreshToken = "initial-refresh"
	oidc := &centralTestOIDC{fakeOIDC: fakeOIDC{exchanged: claims, current: validClaims(now)}, client: client, refreshedExpiry: now.Add(539 * time.Second)}
	service := newTestService(t, repo, oidc, now)
	service.options.CentralClient = client
	service.options.CentralExpectation = central.Expectation{TenantID: "tenant-1", ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}
	service.now = func() time.Time { return now }
	start, err := service.BeginLogin(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AcceptSessionLink(context.Background(), start.State, "platform-ticket"); err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteLogin(context.Background(), start.State, "code")
	if err != nil {
		t.Fatal(err)
	}
	original := now.Add(8 * time.Hour)
	if !result.ExpiresAt.Equal(original) {
		t.Fatal("short token truncated central session")
	}
	row := repo.sessions[tokenHash(result.SessionToken)]
	if string(row.RefreshTokenCipher) == "initial-refresh" || string(row.CentralBindingCipher) == "server-binding" {
		t.Fatal("server secret stored in plaintext")
	}
	now = now.Add(240 * time.Second)
	for range 3 {
		if _, err = service.Authenticate(context.Background(), result.SessionToken); err != nil {
			t.Fatal(err)
		}
	}
	row = repo.sessions[tokenHash(result.SessionToken)]
	if oidc.refreshes != 1 || client.activities != 0 || !row.ExpiresAt.Equal(original) {
		t.Fatal("refresh changed activity or original lifetime")
	}
	if value, err := service.codec.decrypt(row.RefreshTokenCipher); err != nil || value != "rotated-refresh" {
		t.Fatal("rotated refresh token was not persisted")
	}
	client.err = central.ErrUnavailable
	if _, err = service.Authenticate(context.Background(), result.SessionToken); !errors.Is(err, ErrAuthorizationUnavailable) || row.RevokedAt != nil {
		t.Fatal("central outage discarded session")
	}
	client.err = central.ErrInactive
	if _, err = service.Authenticate(context.Background(), result.SessionToken); !errors.Is(err, ErrUnauthenticated) || repo.sessions[tokenHash(result.SessionToken)].RevokedAt == nil {
		t.Fatal("expired central session remained usable")
	}
}
