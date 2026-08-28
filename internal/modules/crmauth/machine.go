package crmauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/applicationjwt"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
)

const machineRequestSkew = 5 * time.Minute

type MachineOptions struct {
	Issuer, Audience, PublicKeyPath, TenantID string
}

type MachineAuthenticator struct {
	verifier *applicationjwt.Verifier
	db       *gorm.DB
	options  MachineOptions
	now      func() time.Time
}

type machineClaims struct {
	Subject, OAuthClientID, TenantID, TokenUse string
	Scopes                                     []string
}

type MachineRequestReplay struct {
	ReplayHash string `gorm:"primaryKey;size:64"`
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

func (MachineRequestReplay) TableName() string { return "crm_machine_request_replays" }

func NewMachineAuthenticator(_ context.Context, db *gorm.DB, options MachineOptions) (*MachineAuthenticator, error) {
	verifier, err := applicationjwt.LoadVerifier(options.Issuer, options.Audience, options.PublicKeyPath)
	if err != nil {
		return nil, err
	}
	return &MachineAuthenticator{verifier: verifier, db: db, options: options, now: time.Now}, nil
}

// Authenticate 校验平台 client_credentials 令牌并消费请求 nonce。机器 Principal 的权限仅来自签名 scope，
// 不读取浏览器角色或业务自定义请求头，避免两套身份语义混用。
func (a *MachineAuthenticator) Authenticate(ctx context.Context, request *http.Request) (sharedauth.Principal, error) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	verified, err := a.verifier.Verify(parts[1], a.now().UTC())
	if err != nil {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	claims := machineClaims{Subject: verified.Subject, OAuthClientID: verified.OAuthClientID, TenantID: verified.TenantID, TokenUse: "application", Scopes: verified.Scopes}
	// 平台协议中 sub 是公开 client_id，oauth_client_id 是注册表记录 ID；二者是不同且都必须存在的绑定。
	if err := validateMachineClaims(claims, a.options.TenantID); err != nil {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	permissions := make(map[string]struct{}, len(claims.Scopes))
	for _, scope := range claims.Scopes {
		permissions[scope] = struct{}{}
	}
	now := a.now().UTC()
	timestamp, err := time.Parse(time.RFC3339Nano, request.Header.Get("X-Integration-Timestamp"))
	nonce := strings.TrimSpace(request.Header.Get("X-Integration-Nonce"))
	if err != nil || nonce == "" || len(nonce) > 200 || now.Sub(timestamp.UTC()) > machineRequestSkew || timestamp.UTC().Sub(now) > machineRequestSkew {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	replayHash := tokenHash(claims.TenantID + "\x00" + claims.OAuthClientID + "\x00" + claims.Subject + "\x00" + nonce)
	// 时间戳限制重放窗口，数据库唯一摘要负责跨实例“只消费一次”；分隔符避免不同字段拼接产生同一摘要输入。
	if err := a.db.WithContext(ctx).Where("expires_at <= ?", now).Delete(&MachineRequestReplay{}).Error; err != nil {
		return sharedauth.Principal{}, err
	}
	result := a.db.WithContext(ctx).Create(&MachineRequestReplay{ReplayHash: replayHash, ExpiresAt: now.Add(machineRequestSkew), CreatedAt: now})
	if result.Error != nil {
		if isDuplicateReplay(result.Error) {
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		return sharedauth.Principal{}, result.Error
	}
	return sharedauth.Principal{UserID: "machine:" + claims.Subject, TenantID: claims.TenantID, Roles: []string{"machine"}, Permissions: permissions, ScopeMode: sharedauth.ScopeAll}, nil
}

func isDuplicateReplay(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &mysqlErr) && mysqlErr.Number == 1062)
}

func validateMachineClaims(claims machineClaims, expectedTenant string) error {
	if claims.TokenUse != "application" || claims.TenantID != expectedTenant || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.OAuthClientID) == "" || len(claims.Scopes) == 0 {
		return ErrUnauthenticated
	}
	for _, scope := range claims.Scopes {
		if !validMachinePermission(scope) {
			return ErrUnauthenticated
		}
	}
	return nil
}

func validMachinePermission(scope string) bool {
	switch scope {
	case "portal.invite.verify", "customer.summary.read", "approval.callback.write", "opportunity.status.write", "opportunity.attachment.scan.write", "customer.credit.payment.ingest", "customer.credit.internal.read":
		return true
	default:
		return false
	}
}
