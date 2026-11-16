package opportunity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegateway"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/handover"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sidebarSessionFixture struct{ principal auth.Principal }

// Authenticate 验证隔离测试Cookie，真实会话认证中间件仍负责接收与拒绝。
// 参数：ctx 为请求上下文，cookie 为本测试服务器的固定夹具令牌。
// 返回值：夹具主体或认证失败；不读取、签发或修改现行平台会话。
func (f sidebarSessionFixture) Authenticate(ctx context.Context, cookie string) (auth.Principal, error) {
	p := f.principal
	switch cookie {
	case "sidebar-qa-session":
		return p, nil
	case "sidebar-qa-read-only":
		p.Permissions = map[string]struct{}{"opportunity.read": {}}
		return p, nil
	case "sidebar-qa-other-tenant":
		p.TenantID = "qa-other-tenant"
		return p, nil
	default:
		return auth.Principal{}, errors.New("测试会话无效")
	}
}

type sidebarScenario struct {
	db          *gorm.DB
	opportunity Opportunity
	principal   auth.Principal
	now         time.Time
	server      *httptest.Server
}

// sidebarDatabase 打开显式指定的一次性MySQL，防止误用任何现行业务数据库。
// 参数：t 为测试上下文，DSN及服务器UUID仅由独立测试容器运行器注入。
// 返回值：已核对实例身份的数据库连接；没有隔离配置时跳过。
func sidebarDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn, expectedUUID := os.Getenv("CRM_SIDEBAR_TEST_DSN"), os.Getenv("CRM_SIDEBAR_TEST_SERVER_UUID")
	if dsn == "" || expectedUUID == "" {
		t.Skip("需要本任务独立MySQL的CRM_SIDEBAR_TEST_DSN及实例UUID")
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("隔离测试DSN格式无效")
	}
	host, _, err := net.SplitHostPort(config.Addr)
	if err != nil || config.Net != "tcp" || host != "127.0.0.1" || config.DBName != "crm_sidebar_qa" {
		t.Fatal("仅允许显式创建的回环crm_sidebar_qa测试库")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var actualUUID string
	if err = db.Raw("SELECT @@server_uuid").Scan(&actualUUID).Error; err != nil || actualUUID != expectedUUID {
		t.Fatal("测试MySQL实例身份与创建记录不一致")
	}
	return db
}

// sidebarSeedOpportunity 在隔离库创建合成客户与商机，区分历史写入人与实际操作人。
// 参数：t 为测试上下文，db 为专用MySQL，p 为测试所有者，now 为固定业务时刻。
// 返回值：仅属于本用例的新商机，不复用现有客户或业务数据。
func sidebarSeedOpportunity(t *testing.T, db *gorm.DB, p auth.Principal, now time.Time) Opportunity {
	t.Helper()
	number := fmt.Sprintf("QA%d", time.Now().UnixNano())
	customer := map[string]any{
		"tenant_id": p.TenantID, "customer_no": number, "name": "QA合成客户",
		"normalized_name": "qa合成客户", "customer_type": "OTHER", "industry": "QA",
		"region": "QA", "owner_user_id": p.UserID, "owner_org_id": p.PrimaryOrgID,
		"status": "ACTIVE", "created_by": "fixture-loader", "updated_by": "fixture-loader",
		"created_at": now, "updated_at": now, "version": 1,
	}
	if err := db.Table("crm_customers").Create(customer).Error; err != nil {
		t.Fatal(err)
	}
	var customerID uint64
	if err := db.Table("crm_customers").Select("id").
		Where("tenant_id=? AND customer_no=?", p.TenantID, number).Scan(&customerID).Error; err != nil {
		t.Fatal(err)
	}
	model := Opportunity{Model: database.Model{TenantID: p.TenantID, CreatedBy: "fixture-loader",
		UpdatedBy: "fixture-loader", Version: 1, CreatedAt: now, UpdatedAt: now},
		OpportunityNo: number, Name: "QA独立商机", CustomerID: customerID, Type: "NEW",
		Source: "INBOUND", ExpectedAmount: decimal.NewFromInt(100), ExpectedSignDate: now,
		RequirementSummary: "仅用于一次性隔离验证", OwnerUserID: p.UserID, OwnerOrgID: p.PrimaryOrgID,
		CurrentStage: StageRequirement, Status: StatusFollowing, ContractLinkStatus: "PENDING",
		ContractSyncVersion: 1, TerminalPendingType: PendingNone, StageChangedAt: now}
	if err := db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	return model
}

// newSidebarScenario 组装实际路由、权限、事务仓储及本地文件网关的隔离场景。
// 参数：t 为测试上下文；认证器和平台目录为明确的外部边界夹具。
// 返回值：回环HTTP服务器及合成商机；不会启动或重建任何现行服务。
func newSidebarScenario(t *testing.T) *sidebarScenario {
	t.Helper()
	db := sidebarDatabase(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := auth.Principal{TenantID: fmt.Sprintf("qa-sidebar-%d", now.UnixNano()),
		UserID: "qa-operator", DisplayName: "QA操作人员", PrimaryOrgID: "qa-org",
		Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf, OrganizationIDs: []string{"qa-org"},
		Permissions: map[string]struct{}{"opportunity.read": {}, "opportunity.update": {},
			"opportunity.team.manage": {}, "opportunity.attachment.read": {},
			"opportunity.attachment.upload": {}, "opportunity.attachment.download": {}}}
	scenario := &sidebarScenario{db: db, principal: p, now: now}
	scenario.opportunity = sidebarSeedOpportunity(t, db, p, now)
	repo, writer := NewGORMRepository(db), audit.NewGORMWriter(db)
	service := NewService(db, repo, writer, nil)
	service.now = func() time.Time { return now }
	freeze, err := handover.NewGORMRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	service.UseFreezeChecker(freeze)
	service.UseOwnerDirectory(teamDirectoryStub{users: map[string]ownerdirectory.User{
		"qa-helper": {ID: "qa-helper", DisplayName: "QA辅助人员",
			Organizations: []ownerdirectory.Organization{{ID: "qa-org", Name: "QA组织", IsPrimary: true}}},
	}})
	local, err := filegateway.NewLocalClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := NewLocalFileGatewayStore(local)
	attachments := NewAttachmentService(NewGORMAttachmentRepository(db), repo, writer,
		store, NewCodeAttachmentScanner(store), 0)
	router := gin.New()
	router.Use(middleware.RequestID())
	group := router.Group("/api/v1", middleware.SessionAuth(sidebarSessionFixture{p}, "qa_session"),
		middleware.RequireSameOriginWrite("http://crm-sidebar.test"))
	RegisterRoutes(group, NewHandler(service).UseAttachments(attachments))
	scenario.server = httptest.NewServer(router)
	t.Cleanup(scenario.server.Close)
	return scenario
}

// call 通过回环HTTP执行一个受现有会话、同源和精确权限约束的请求。
// 参数：t 为测试上下文，method/suffix 为接口，body 为合成内容，session 为夹具会话，want 为状态。
// 返回值：有界响应原字节；意外状态会输出本用例安全回执并失败。
func (s *sidebarScenario) call(t *testing.T, method, suffix string, body []byte, session string, want int) []byte {
	t.Helper()
	path := fmt.Sprintf("/api/v1/opportunities/%d%s", s.opportunity.ID, suffix)
	req, err := http.NewRequestWithContext(t.Context(), method, s.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://crm-sidebar.test")
	req.Header.Set("X-CSRF-Token", "1")
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPut && len(suffix) >= 8 && suffix[len(suffix)-8:] == "/content" {
		req.Header.Set("Content-Type", "application/pdf")
	}
	req.Header.Set("Idempotency-Key", "qa-sidebar-"+method)
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "qa_session", Value: session})
	}
	response, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != want {
		t.Fatalf("%s %s 状态=%d，预期=%d，回执=%s，错误=%v", method, suffix, response.StatusCode, want, raw, err)
	}
	return raw
}

// sidebarJSON 将合成请求编码为业务接口原JSON，不替换实际DTO校验。
// 参数：t 为测试上下文，value 为待提交数据。
// 返回值：JSON字节；编码失败时终止测试。
func sidebarJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// sidebarData 读取实际接口成功信封，避免把错误或空成功回执当成完成。
// 参数：t 为测试上下文，raw 为HTTP响应原字节。
// 返回值：类型化业务data；信封无效时失败。
func sidebarData[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var envelope struct {
		Code string `json:"code"`
		Data T      `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Code != "OK" {
		t.Fatalf("成功信封无效：%s，错误=%v", raw, err)
	}
	return envelope.Data
}

// sidebarMemberPayload 返回最小单成员替换命令，保持现有版本与变更原因契约。
// 参数：t 为测试上下文。
// 返回值：只有一位合成辅助成员的请求字节。
func sidebarMemberPayload(t *testing.T) []byte {
	t.Helper()
	return sidebarJSON(t, ReplaceMembersRequest{Version: 1, Reason: "QA单成员验证",
		Members: []TeamMemberInput{{UserID: "qa-helper", Role: MemberRoleTechnicalSupport}}})
}
