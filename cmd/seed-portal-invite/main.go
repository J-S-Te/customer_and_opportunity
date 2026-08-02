// Command seed-portal-invite provisions a real Portal customer account through
// the production CM-004 saga: it creates the platform external user (no local
// password), assigns customer_portal/portal_customer, registers the Portal
// identity mapping and returns the one-time activation link. Use it only on a
// local development stack that has completed the Portal onboarding.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/bootstrap"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	customerFilter := flag.String("customer", "", "customer name or customer_no; defaults to the first ACTIVE customer with a registration contact")
	idempotencyKey := flag.String("key", "", "idempotency key; defaults to seed-portal-invite-<customer-id>")
	actorID := flag.String("actor-id", "oidc-sub-demo-seed", "seed actor OIDC subject that owns the provision operation")
	actorName := flag.String("actor-name", "演示数据初始化", "seed actor display name")
	flag.Parse()

	config, err := bootstrap.LoadConfig()
	if err != nil {
		fatalf("load config: %v", err)
	}
	if !config.PortalInviteEnabled {
		fatalf("PORTAL_INVITE_ENABLED=false: the Portal invitation saga is not configured")
	}
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		fatalf("open database: %v", err)
	}
	codec, err := security.NewSensitiveCodec(config.EncryptionKey, config.HMACKey)
	if err != nil {
		fatalf("sensitive codec: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err = db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		fatalf("connect to database: %v", err)
	}

	model, err := findCustomer(ctx, db, config.OIDCTenantID, strings.TrimSpace(*customerFilter))
	if err != nil {
		fatalf("find customer: %v", err)
	}
	key := strings.TrimSpace(*idempotencyKey)
	if key == "" {
		key = fmt.Sprintf("seed-portal-invite-%d", model.ID)
	}
	actor := strings.TrimSpace(*actorID)
	if actor == "" {
		fatalf("actor id must not be empty")
	}
	principal := auth.Principal{
		TenantID: config.OIDCTenantID, UserID: actor, DisplayName: strings.TrimSpace(*actorName),
		Permissions: map[string]struct{}{"portal_account.provision": {}}, ScopeMode: auth.ScopeAll,
	}
	actorCtx := auth.WithPrincipal(ctx, principal)

	platformProvisioner, err := portalinvite.NewHTTPPlatformProvisioner(context.Background(), portalinvite.PlatformProvisionerOptions{
		ProvisionURL: config.PlatformExternalUserProvisionURL, RoleAssignURL: config.PlatformApplicationRoleAssignURL,
		TokenURL: config.PlatformManagementTokenURL, ApplicationCode: config.PlatformPortalApplicationCode,
		ProvisionClientID: config.PlatformExternalUserClientID, ProvisionClientSecret: config.PlatformExternalUserClientSecret,
		ProvisionScope: config.PlatformExternalUserScope, RoleClientID: config.PlatformRoleAssignClientID,
		RoleClientSecret: config.PlatformRoleAssignClientSecret, RoleScope: config.PlatformRoleAssignScope,
		TLS: config.PlatformManagementTLS,
	})
	if err != nil {
		fatalf("platform provisioner: %v", err)
	}
	portalProvisioner, err := portalinvite.NewHTTPPortalProvisioner(context.Background(), portalinvite.PortalProvisionerOptions{
		Endpoint: config.PortalProvisionURL, TokenURL: config.PortalProvisionTokenURL,
		ClientID: config.PortalProvisionClientID, ClientSecret: config.PortalProvisionClientSecret,
		Scope: config.PortalProvisionScope, TLS: config.PortalProvisionTLS,
	})
	if err != nil {
		fatalf("portal provisioner: %v", err)
	}
	service := portalinvite.NewService(
		portalinvite.NewGORMRepository(db), portalinvite.NewCustomerAdapter(db, codec),
		platformProvisioner, portalProvisioner, audit.NewGORMWriter(db),
		config.PortalInvitePepper, config.PortalPublicURL,
		portalinvite.SystemClock{}, portalinvite.CryptoRandom{}, inviteProtector{codec: codec},
	)
	result, err := service.Create(actorCtx, model.ID, portalinvite.CreateRequest{IdempotencyKey: key})
	if err != nil {
		fatalf("provision portal account for %s (id=%d): %v", model.Name, model.ID, err)
	}
	fmt.Printf("customer=%s id=%d\n", model.Name, model.ID)
	fmt.Printf("invite_no=%s\n", result.InviteNo)
	fmt.Printf("login_account=%s\n", result.LoginAccount)
	fmt.Printf("status=%s expires_at=%s\n", result.Status, result.ExpiresAt.Format(time.RFC3339))
	fmt.Printf("activation_url=%s\n", result.ActivationURL)
	fmt.Println("下一步：在基础平台“系统设置 → 登录账号”对 login_account 执行“初始化密码”，再用激活链接登录 Portal。")
}

func findCustomer(ctx context.Context, db *gorm.DB, tenantID, filter string) (customer.Customer, error) {
	query := db.WithContext(ctx).Where("tenant_id = ? AND status = ? AND deleted_at IS NULL", tenantID, customer.StatusActive)
	if filter != "" {
		query = query.Where("name = ? OR customer_no = ?", filter, filter)
	}
	var model customer.Customer
	if err := query.Order("id ASC").First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return customer.Customer{}, fmt.Errorf("no ACTIVE customer found for filter %q", filter)
		}
		return customer.Customer{}, err
	}
	return model, nil
}

// inviteProtector mirrors the bootstrap portal-invite operation protector so
// provision snapshots stay encrypted outside the portalinvite module.
type inviteProtector struct {
	codec *security.SensitiveCodec
}

func (p inviteProtector) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	return p.codec.Encrypt(string(plaintext))
}

func (p inviteProtector) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := p.codec.Decrypt(ciphertext)
	return []byte(plaintext), err
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
