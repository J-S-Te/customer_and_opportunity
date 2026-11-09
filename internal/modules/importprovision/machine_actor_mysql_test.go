package importprovision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/handover"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type machineActorCatalog struct{}

// List 提供合成负责人目录，业务持久化仍使用真实 MySQL。
// 参数：上下文及负责人查询条件。
// 返回值：唯一有效的测试负责人及其主组织。
func (machineActorCatalog) List(context.Context, ownerdirectory.Query) (ownerdirectory.Page, error) {
	return ownerdirectory.Page{Items: []ownerdirectory.User{{
		ID: "owner-test", Organizations: []ownerdirectory.Organization{
			{ID: "org-test", IsPrimary: true},
		},
	}}}, nil
}

// Validate 接受合成目录中的负责人。
// 参数：上下文、人员及组织标识。
// 返回值：无错误；本测试不替代目录边界测试。
func (machineActorCatalog) Validate(context.Context, string, string) error { return nil }

// Resolve 满足目录接口，本场景不查询展示姓名。
// 参数：上下文及待解析身份。
// 返回值：空映射及无错误。
func (machineActorCatalog) Resolve(context.Context, []string) (map[string]ownerdirectory.User, error) {
	return nil, nil
}

type machineActorContractVerifier struct{}

// BelongsToCustomer 模拟受控的合同验真依赖。
// 参数：上下文、已预留的合同引用及客户编号。
// 返回值：验真通过；不写外部合同数据。
func (machineActorContractVerifier) BelongsToCustomer(context.Context, string, uint64) (bool, error) {
	return true, nil
}

// machineActorTestDB 只连接显式指定的回环隔离测试库。
// 参数：t 为测试上下文，CRM_TEST_DSN 必须指向 crm_test_ 前缀数据库。
// 返回值：真实 MySQL 句柄；缺少配置时跳过，越界配置直接失败。
func machineActorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("CRM_TEST_DSN")
	if dsn == "" {
		t.Skip("需要显式隔离 CRM_TEST_DSN")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") ||
		!strings.HasPrefix(cfg.DBName, "crm_test_") {
		t.Fatal("仅允许回环 crm_test_ 隔离数据库")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("隔离 MySQL 连接失败")
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return db
}

// machineActorContext 构造仅含历史开立权限的合成机器身份。
// 参数：tenant 隔离测试租户，actor 为需要完整保留的机器主体。
// 返回值：可信上下文替身，不授予普通客户或商机创建权限。
func machineActorContext(tenant, actor string) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{
		TenantID: tenant, UserID: actor, Roles: []string{"machine"}, ScopeMode: auth.ScopeAll,
		Permissions: map[string]struct{}{customer.ProvisionScope: {}, opportunity.ProvisionScope: {}},
	})
}

// machineActorCustomerService 装配真实客户仓储和包含平台发件箱的审计写入器。
// 参数：t 为测试上下文，db 为隔离事务句柄。
// 返回值：仅目录边界使用替身的客户服务。
func machineActorCustomerService(t *testing.T, db *gorm.DB) *customer.Service {
	t.Helper()
	codec, err := security.NewSensitiveCodec(
		[]byte("01234567890123456789012345678901"), []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	writer := audit.NewGORMWriter(db).UsePlatformOutbox(requestaudit.NewStore(db), "test")
	freeze, err := handover.NewGORMRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	return customer.NewService(db, customer.NewGORMRepository(db), writer, codec).
		UseOwnerDirectory(machineActorCatalog{}).UseFreezeChecker(freeze)
}

// machineActorCustomerInput 提供可通过真实客户校验的合成请求。
// 参数：无。
// 返回值：固定幂等键及非真实联系人数据。
func machineActorCustomerInput() customer.ProvisionCreateRequest {
	return customer.ProvisionCreateRequest{
		Name: "机器主体长度回归客户", OwnerIdentityID: "owner-test", ActorIdentityID: "operator-test",
		IdempotencyKey: "customer-stable-key", ExternalRecordID: "legacy-test",
		Contacts: []customer.ContactInput{{Name: "测试联系人", Phone: "13800000000", IsRegistration: true}},
	}
}

// TestProvisionMachineActorOnMySQL 验证长机器主体完整落库、同键重放及不同主体隔离。
// 参数：t 为测试上下文；运行前仅在隔离库应用 CRM 正式迁移清单。
// 返回值：客户、商机、签约或审计任一环节截断、重复或串用身份时失败。
func TestProvisionMachineActorOnMySQL(t *testing.T) {
	db := machineActorTestDB(t)
	for _, length := range []int{64, 65, 128} {
		for _, resource := range []string{"opportunity", "customer"} {
			t.Run(fmt.Sprintf("%s/actor-%d", resource, length), func(t *testing.T) {
				tx := db.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				t.Cleanup(func() { _ = tx.Rollback().Error })
				tenant := fmt.Sprintf("actor-test-%d", time.Now().UnixNano())
				actor := "machine:" + strings.Repeat("x", length-9) + "a"
				ctx := machineActorContext(tenant, actor)
				customers := machineActorCustomerService(t, tx)
				if resource == "customer" {
					verifyMachineCustomerReplay(t, tx, customers, ctx, tenant, actor)
				} else {
					verifyMachineOpportunityReplay(t, tx, customers, ctx, tenant, actor)
				}
				var events []audit.Event
				if err := tx.Where("tenant_id=? AND actor_id=?", tenant, actor).Find(&events).Error; err != nil || len(events) == 0 {
					t.Fatalf("完整机器主体审计缺失: count=%d err=%v", len(events), err)
				}
				var count int64
				if err := tx.Model(&requestaudit.Record{}).
					Where("tenant_id=? AND actor_id=? AND actor_type='MACHINE'", tenant, actor).
					Count(&count).Error; err != nil || count != int64(len(events)) {
					t.Fatalf("平台审计发件箱未原样保留机器身份: count=%d err=%v", count, err)
				}
			})
		}
	}
}

// verifyMachineCustomerReplay 验证客户同键重放及完整主体隔离。
// 参数：测试上下文、隔离事务、真实客户服务及合成身份。
// 返回值：无；重复创建或长主体串用时测试失败。
func verifyMachineCustomerReplay(t *testing.T, db *gorm.DB, service *customer.Service,
	ctx context.Context, tenant, actor string) {
	t.Helper()
	input := machineActorCustomerInput()
	first, err := service.CreateProvision(ctx, input)
	if err != nil {
		t.Fatalf("客户开立失败: %v", err)
	}
	second, err := service.CreateProvision(ctx, input)
	if err != nil || first.ID != second.ID || first.CustomerNo != second.CustomerNo {
		t.Fatalf("客户重放错误: err=%v", err)
	}
	var idem customer.CreateIdempotency
	if err = db.Where("tenant_id=?", tenant).Take(&idem).Error; err != nil || idem.ActorID != actor || idem.CustomerID != first.ID {
		t.Fatalf("客户幂等身份未完整保存: %v", err)
	}
	input.Name = "变更后的客户"
	if _, err = service.CreateProvision(ctx, input); !errors.Is(err, customer.ErrIdempotencyConflict) {
		t.Fatalf("同键换载荷未拒绝: %v", err)
	}
	input.Name = "另一个机器主体的客户"
	other := machineActorContext(tenant, strings.TrimSuffix(actor, "a")+"b")
	if result, createErr := service.CreateProvision(other, input); createErr != nil || result.ID == first.ID {
		t.Fatalf("不同机器主体同键串用: %v", createErr)
	}
}

// verifyMachineOpportunityReplay 验证商机创建重放、签约重放及完整主体隔离。
// 参数：测试上下文、隔离事务、客户服务及待验证的机器身份。
// 返回值：无；任一步失败、重复创建或主体串用时测试失败。
func verifyMachineOpportunityReplay(t *testing.T, db *gorm.DB, customers *customer.Service,
	ctx context.Context, tenant, actor string) {
	t.Helper()
	seed, err := customers.CreateProvision(machineActorContext(tenant, "machine:seed"), machineActorCustomerInput())
	if err != nil {
		t.Fatal(err)
	}
	writer := audit.NewGORMWriter(db).UsePlatformOutbox(requestaudit.NewStore(db), "test")
	freeze, err := handover.NewGORMRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	service := opportunity.NewService(db, opportunity.NewGORMRepository(db), writer,
		machineActorContractVerifier{}).UseOwnerDirectory(machineActorCatalog{}).UseFreezeChecker(freeze)
	input := opportunity.ProvisionCreateRequest{
		Name: "机器主体长度回归商机", CustomerID: seed.ID, ExpectedAmount: "50000.00",
		ExpectedSignDate: "2026-01-02", RequirementSummary: "合成历史合同",
		OwnerIdentityID: "owner-test", ActorIdentityID: "operator-test", IdempotencyKey: "opportunity-key",
	}
	first, err := service.CreateProvision(ctx, input)
	if err != nil {
		t.Fatalf("商机开立失败: %v", err)
	}
	second, err := service.CreateProvision(ctx, input)
	if err != nil || second.ID != first.ID || second.OpportunityNo != first.OpportunityNo {
		t.Fatalf("商机重放错误: %v", err)
	}
	var idem opportunity.CreateIdempotency
	if err = db.Where("tenant_id=?", tenant).Take(&idem).Error; err != nil || idem.ActorID != actor || idem.OpportunityID != first.ID {
		t.Fatalf("商机幂等身份未完整保存: %v", err)
	}
	input.Name = "变更后的商机"
	if _, err = service.CreateProvision(ctx, input); !errors.Is(err, opportunity.ErrIdempotencyConflict) {
		t.Fatalf("商机同键换载荷未拒绝: %v", err)
	}
	other := machineActorContext(tenant, strings.TrimSuffix(actor, "a")+"b")
	if result, createErr := service.CreateProvision(other, input); createErr != nil || result.ID == first.ID {
		t.Fatalf("不同机器主体同键串用: %v", createErr)
	}
	sign := opportunity.ProvisionSignRequest{ContractRef: "contract-test", ContractID: "contract-test",
		ExpectedVersion: first.Version, ActorIdentityID: "operator-test", Reason: "隔离历史签约回归"}
	closed, _, err := service.SignProvision(ctx, first.ID, sign)
	if err != nil || closed.CurrentStage != opportunity.StageSigned || closed.Status != opportunity.StatusClosed {
		t.Fatalf("商机签约失败: %v", err)
	}
	if repeated, replay, signErr := service.SignProvision(ctx, first.ID, sign); signErr != nil || !replay || repeated.Version != closed.Version {
		t.Fatalf("商机签约未幂等: %v", signErr)
	}
}
