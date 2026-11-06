package localidentity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
)

const (
	PermissionCustomerManage = "portal.customer.manage"
	PermissionInviteManage   = "portal.invite.manage"
	PermissionAccessDisable  = "portal.access.disable"
	PermissionProjectManage  = "portal.local_project.manage"
	PermissionReportManage   = "portal.local_report.manage"
)

type ContactInput struct {
	CustomerID  uint64 `json:"customer_id"`
	DisplayName string `json:"display_name"`
	Phone       string `json:"phone"`
}
type PlatformIdentity struct {
	PlatformUserID string `json:"platform_user_id"`
	AccountNo      string `json:"account_no"`
}
type Platform interface {
	Provision(context.Context, string, ContactInput) (PlatformIdentity, error)
	AssignRole(context.Context, string, string) error
	BindCustomer(context.Context, string, uint64, string) error
	RevokeRole(context.Context, string, string) error
	DisableBinding(context.Context, string, uint64, string) error
}
type AuditPublisher interface {
	Publish(context.Context, requestaudit.BusinessEvent) error
}
type Dependencies struct {
	Repo                *Repository
	Accounts            *account.Service
	Protector           account.SecretProtector
	DescriptorProtector interface {
		Encrypt(context.Context, []byte) ([]byte, error)
	}
	HMACKey         []byte
	Platform        Platform
	Audit           AuditPublisher
	Mode            Mode
	TenantID        string
	EnvironmentCode string
	PublicURL       string
	CheckLicense    func(context.Context) error
	Files           LocalReportFiles
}
type Service struct {
	Dependencies
	now    func() time.Time
	random io.Reader
}

// NewService 装配本地门户业务，不将缺失平台或文件依赖解释为成功。
// 参数：deps 为门户自己的仓储、真实平台客户端与安全依赖。
// 返回值：服务；基础安全配置缺失时失败。
func NewService(deps Dependencies) (*Service, error) {
	if deps.Repo == nil || deps.Repo.db == nil || deps.Protector == nil || len(deps.HMACKey) < 32 || deps.TenantID == "" || deps.Audit == nil || deps.CheckLicense == nil || deps.Mode != ModeLocal {
		return nil, ErrUnavailable
	}
	return &Service{Dependencies: deps, now: func() time.Time { return time.Now().UTC() }, random: rand.Reader}, nil
}

// principal 限定真实门户超管与精确管理权限，不按平台管理员名推断客户授权。
// 参数：ctx 为已认证上下文，permission 为本操作权限，write 表示真实业务写。
// 返回值：可信主体或权限、模式及许可错误。
func (s *Service) principal(ctx context.Context, permission string, write bool) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.TenantID != s.TenantID || p.UserID == "" || len(p.Roles) != 1 || p.Roles[0] != "portal_super_admin" || !p.HasPermission(permission) {
		return p, apperror.ErrForbidden
	}
	all := false
	for _, scope := range p.DataScopes {
		if scope.RoleCode == "portal_super_admin" && ((scope.ScopeType == "APPLICATION" || scope.ScopeType == "TENANT") && scope.ScopeID == "" || scope.ScopeType == "ENVIRONMENT" && scope.ScopeID != "" && scope.EnvironmentCode == s.EnvironmentCode) {
			all = true
		}
	}
	if !all {
		return p, apperror.ErrForbidden
	}
	if err := s.Repo.CheckMode(ctx, p.TenantID, ModeLocal); err != nil {
		return p, err
	}
	if write {
		if err := s.CheckLicense(ctx); err != nil {
			return p, err
		}
	}
	return p, nil
}

// record 为本地业务记录生成可信审计坐标。
// 参数：p 为当前主体。
// 返回值：服务端记录头。
func (s *Service) record(p auth.Principal) Record {
	now := s.now()
	return Record{TenantID: p.TenantID, Version: 1, CreatedBy: p.UserID, UpdatedBy: p.UserID, CreatedAt: now, UpdatedAt: now}
}

// digest 计算分用途HMAC，不持久化邀请令牌或手机明文。
// 参数：kind 为用途域，value 为秘密或业务输入。
// 返回值：十六进制认证摘要。
func (s *Service) digest(kind, value string) string { return keyedDigest(s.HMACKey, kind, value) }

// keyedDigest 为同一部署的安全令牌建立用途隔离。
// 参数：key 为受控秘密，kind/value 为用途及原始材料。
// 返回值：HMAC-SHA256摘要。
func keyedDigest(key []byte, kind, value string) string {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(kind + "\x00" + value))
	return hex.EncodeToString(h.Sum(nil))
}

// token 生成真实高熵业务编号或一次性邀请材料。
// 参数：size 为随机字节数。
// 返回值：十六进制随机材料或随机源错误。
func (s *Service) token(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// audit 在当前事务内记录门户事实，不写客户敏感资料。
// 参数：ctx/p 为请求主体，action/resource/id 为业务审计坐标。
// 返回值：审计持久化错误。
func (s *Service) audit(ctx context.Context, p auth.Principal, action, resource, id string) error {
	return s.Audit.Publish(ctx, requestaudit.BusinessEvent{EventID: requestctx.NewID(), TenantID: p.TenantID, ApplicationCode: "customer_portal", EnvironmentCode: s.EnvironmentCode, ActorType: "USER", ActorID: p.UserID, Action: action, ResourceType: resource, ResourceID: id, RequestID: requestctx.ID(ctx), Result: "SUCCESS", RiskLevel: "HIGH", OccurredAt: s.now()})
}

// requestHash 为非秘密业务请求建立稳定载荷摘要。
// 参数：value 为只用于幂等的完整请求。
// 返回值：SHA256摘要。
func requestHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// validText 限制业务文字长度并拒绝控制字符。
// 参数：value 为输入，max 为字符上限，required 表示是否必填。
// 返回值：输入是否满足有界安全文本规则。
func validText(value string, max int, required bool) bool {
	return (!required || strings.TrimSpace(value) != "") && len([]rune(value)) <= max && !strings.ContainsAny(value, "\x00\r\n\t")
}
