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

// Authenticate verifies a platform client-credentials token and consumes the
// caller's request nonce. The signed scope becomes the machine Principal's only
// permission set; browser roles and headers are never considered.
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
	// Platform contract: sub is the public client_id while oauth_client_id is
	// the registry record ID. They are distinct required bindings.
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
	case "portal.invite.verify", "customer.summary.read", "approval.callback.write", "opportunity.status.write", "opportunity.attachment.scan.write":
		return true
	default:
		return false
	}
}
