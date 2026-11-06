package localidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
)

type reservationPlatform struct {
	localFlowPlatform
	roles         atomic.Int32
	binds         atomic.Int32
	boundCustomer atomic.Uint64
}

// AssignRole 记录真实副作用边界的合成端口，用于检查持久预留发生顺序。
// 参数：上下文、主体及原操作号。
// 返回值：受控角色故障或空错误。
func (p *reservationPlatform) AssignRole(context.Context, string, string) error {
	p.roles.Add(1)
	if p.failRole.Load() {
		return errors.New("role unavailable")
	}
	return nil
}

// BindCustomer 记录实际收到的客户归属，不能把端口成功当作平台唯一约束证明。
// 参数：上下文、平台主体、真实客户及操作号。
// 返回值：空错误；平台唯一约束另由真实91迁移用例验证。
func (p *reservationPlatform) BindCustomer(_ context.Context, _ string, customer uint64, _ string) error {
	p.binds.Add(1)
	p.boundCustomer.Store(customer)
	return nil
}

// TestLocalInvitationSubjectReservationMySQL 验证失败恢复不能在另一客户之前窃取同一真实主体。
// 参数：t为独立高端口空库测试上下文。
// 返回值：无；缺少持久预留、越权副作用或原操作不能恢复时失败。
func TestLocalInvitationSubjectReservationMySQL(t *testing.T) {
	repo := foundationDB(t)
	platform := &reservationPlatform{}
	platform.failRole.Store(true)
	protector, err := report.NewAESDescriptorProtector(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	accounts := account.NewService(account.NewGORMRepository(repo.db).UseLocalDirectory(), nil, nil, protector, flowClock{}, nil, "catalog", time.Hour)
	service, err := NewService(Dependencies{Repo: repo, Accounts: accounts, Protector: protector, HMACKey: bytes.Repeat([]byte{8}, 32), Platform: platform, Audit: requestaudit.NewPortalStore(repo.db), Mode: ModeLocal, TenantID: "local-tenant", EnvironmentCode: "test", PublicURL: "https://portal.test/customer-portal", CheckLicense: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	permissions := map[string]struct{}{PermissionCustomerManage: {}, PermissionInviteManage: {}}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "local-tenant", UserID: "real-admin", Roles: []string{"portal_super_admin"}, Permissions: permissions, DataScopes: []auth.DataScope{{RoleCode: "portal_super_admin", ScopeType: "APPLICATION"}}})
	a, err := service.CreateCustomer(ctx, CustomerInput{Code: "A", Name: "本地客户A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.CreateCustomer(ctx, CustomerInput{Code: "B", Name: "本地客户B"})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := service.CreateContact(ctx, ContactInput{CustomerID: a.ID, DisplayName: "A联系人", Phone: "13800000000"})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := service.CreateContact(ctx, ContactInput{CustomerID: b.ID, DisplayName: "B联系人", Phone: "13800000000"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateInvitation(ctx, InvitationInput{CustomerID: a.ID, ContactID: ca.ID, ExpectedContactVersion: ca.Version, IdempotencyKey: "reserve-a"})
	if !errors.Is(err, ErrUnavailable) || first.Operation.Stage != "IDENTITY_READY" {
		t.Fatalf("missing retry boundary: %+v %v", first, err)
	}
	link, err := account.NewGORMRepository(repo.db).FindLink(ctx, "local-tenant", "local-real-subject")
	if err != nil || link.CustomerID != a.ID || link.ContactID == nil || *link.ContactID != ca.ID || link.Status != account.IdentityPending {
		t.Fatalf("identity must be durably reserved before role: link=%+v err=%v", link, err)
	}
	platform.failRole.Store(false)
	_, err = service.CreateInvitation(ctx, InvitationInput{CustomerID: b.ID, ContactID: cb.ID, ExpectedContactVersion: cb.Version, IdempotencyKey: "reserve-b"})
	if err == nil || platform.roles.Load() != 1 || platform.binds.Load() != 0 {
		t.Fatalf("other customer reached role/binding: roles=%d binds=%d err=%v", platform.roles.Load(), platform.binds.Load(), err)
	}
	resumed, err := service.ResumeOperation(ctx, first.Operation.OperationNo)
	if err != nil || resumed.Invitation == nil || platform.boundCustomer.Load() != a.ID || platform.binds.Load() != 1 {
		t.Fatalf("original reservation cannot resume: %+v %v", resumed, err)
	}
	ref, err := CustomerBindingRef(a.ID, "local-real-subject")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &AccountAdapter{Repo: repo, TenantID: "local-tenant", HMACKey: bytes.Repeat([]byte{8}, 32)}
	if got, err := adapter.ResolveCustomerRef(ctx, "local-tenant", "local-real-subject", ref, true); err != nil || got != a.ID {
		t.Fatal("pending local reference rejected", got, err)
	}
	for _, bad := range [][3]string{{"other-tenant", "local-real-subject", ref}, {"local-tenant", "other-subject", ref}, {"local-tenant", "local-real-subject", "1"}} {
		if _, err := adapter.ResolveCustomerRef(ctx, bad[0], bad[1], bad[2], true); err == nil {
			t.Fatal("foreign authority accepted", bad)
		}
	}
}

// TestLocalCustomerRefPlatformMigration91MySQL 保持真实平台唯一键并证明同客户多主体引用不会碰撞。
// 参数：t为同一安全门禁下的独立空库上下文；不连接业务库。
// 返回值：无；旧数字碰撞未拒绝、真实91约束缺失或新引用碰撞时失败。
func TestLocalCustomerRefPlatformMigration91MySQL(t *testing.T) {
	repo := foundationDB(t)
	fixture := `CREATE TABLE iam_tenant(id CHAR(26) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY);
 CREATE TABLE iam_external_identity(id CHAR(26) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY);
 CREATE TABLE iam_external_identity_idempotency(operation VARCHAR(32),CONSTRAINT chk_external_identity_idempotency_operation CHECK(operation IN ('PROVISION','ASSIGN_ROLE','REVOKE_ROLE')));
 INSERT INTO iam_tenant VALUES('01ARZ3NDEKTSV4RRFFQ69G5FA0');
 INSERT INTO iam_external_identity VALUES('01ARZ3NDEKTSV4RRFFQ69G5FAV'),('01ARZ3NDEKTSV4RRFFQ69G5FAW');`
	if err := repo.db.Exec(fixture).Error; err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../../../platform/migrations/000091_create_external_customer_binding.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Exec(string(migration)).Error; err != nil {
		t.Fatal("actual platform migration91", err)
	}
	subjects := []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	insert := func(subject, ref string) error {
		// 摘要仅为唯一键fixture，不冒充平台HMAC-SM3/AES-GCM生产实现。
		digest := sha256.Sum256([]byte("tenant\x00customer_portal\x00" + ref))
		return repo.db.Exec(`INSERT INTO iam_external_customer_binding(id,tenant_id,identity_id,application_code,customer_ref_digest,customer_ref_cipher,status,created_at,created_by,updated_at,updated_by) VALUES(?, '01ARZ3NDEKTSV4RRFFQ69G5FA0',?,'customer_portal',?,?,'ACTIVE',NOW(3),?,NOW(3),?)`, subject, subject, digest[:], []byte("synthetic-encrypted-fixture"), subject, subject).Error
	}
	if err := insert(subjects[0], "7"); err != nil {
		t.Fatal(err)
	}
	err = insert(subjects[1], "7")
	var duplicate *driver.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 || !strings.Contains(duplicate.Message, "uk_customer_binding_customer") {
		t.Fatalf("old numeric ref must hit actual unique key: %v", err)
	}
	if err := repo.db.Exec("DELETE FROM iam_external_customer_binding").Error; err != nil {
		t.Fatal(err)
	}
	for _, subject := range subjects {
		ref, err := CustomerBindingRef(7, subject)
		if err != nil {
			t.Fatal(err)
		}
		if err := insert(subject, ref); err != nil {
			t.Fatal("same customer distinct subject failed", err)
		}
	}
	var count int64
	if err := repo.db.Table("iam_external_customer_binding").Count(&count).Error; err != nil || count != 2 {
		t.Fatal("lost binding", count, err)
	}
}
