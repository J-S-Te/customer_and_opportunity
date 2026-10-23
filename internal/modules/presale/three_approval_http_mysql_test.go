package presale

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestThreeApprovalNodesOverRealHTTPAndMySQL 验证真实 Gin 路由、
// 服务、GORM 仓储、迁移 CHECK 约束及审批日志表。
// 数据库必须已应用完整 CRM 迁移链。
func TestThreeApprovalNodesOverRealHTTPAndMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PRESALE_MYSQL_TEST_DSN"))
	if dsn == "" {
		t.Skip("PRESALE_MYSQL_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "qa-three-approval-" + time.Now().UTC().Format("20060102150405.000000000")
	now := time.Now().UTC()
	request := &PresaleRequest{
		BaseModel: BaseModel{TenantID: tenant, CreatedBy: "applicant", UpdatedBy: "applicant", Version: 1, CreatedAt: now, UpdatedAt: now},
		RequestNo: "QA3-" + time.Now().UTC().Format("150405.000000"), OpportunityID: 1, OpportunityNoSnapshot: "OPP-QA3",
		ApplicantID: "applicant", ApplicantNameSnapshot: "申请人", Venue: "OFFICE", ContactName: "联系人",
		ContactPhoneCipher: []byte("cipher"), ContactPhoneMasked: "138****0000", Description: "三审批节点验收",
		ExpectedStart: now, ExpectedEnd: now.Add(time.Hour), Urgency: "NORMAL", Status: StatusPendingApproval,
		CurrentApprovalNode: 1, CreateIdempotencyKey: "create-" + tenant, CreateRequestHash: strings.Repeat("a", 64),
	}
	if err = db.Create(request).Error; err != nil {
		t.Fatal(err)
	}
	nodes, _ := json.Marshal([]ApprovalNode{
		{ID: "node-1", Name: "销售审批", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "node-2", Name: "技术审批", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
		{ID: "node-3", Name: "复核", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
	})
	instance := &ApprovalInstance{
		BaseModel: BaseModel{TenantID: tenant, CreatedBy: "applicant", UpdatedBy: "applicant", Version: 1, CreatedAt: now, UpdatedAt: now},
		RequestID: request.ID, EngineInstanceID: "qa-engine-" + request.RequestNo, Status: "PENDING", CurrentNode: 1,
		NodesJSON: nodes, RuleID: "qa-rule", RuleVersion: 1, StartedAt: &now,
	}
	if err = db.Create(instance).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewGORMRepository(db)
	service := NewService(repo, nil, nil, SystemClock{}, fixedIDs{})
	gin.SetMode(gin.TestMode)

	approvals := []struct {
		user string
		role string
		key  string
	}{
		{user: "sales-1", role: "sales_director", key: "approve-node-1"},
		{user: "tech-1", role: "technical_director", key: "approve-node-2"},
		{user: "sales-2", role: "sales_director", key: "approve-node-3"},
	}
	for index, step := range approvals {
		var current PresaleRequest
		if err = db.First(&current, request.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.Status != StatusPendingApproval || int(current.CurrentApprovalNode) != index+1 {
			t.Fatalf("before step %d request status=%s node=%d version=%d", index+1, current.Status, current.CurrentApprovalNode, current.Version)
		}
		actor := Actor{
			TenantID: tenant, UserID: step.user, UserName: "审批人" + step.user, PersonID: step.user,
			Roles:       map[string]bool{step.role: true, "crm_super_admin": false},
			Permissions: map[string]bool{"presale.approve": true, "presale.read": true},
		}
		handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: actor})
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{
				TenantID: tenant, UserID: step.user, Permissions: map[string]struct{}{"presale.approve": {}, "presale.read": {}},
			}))
			c.Next()
		})
		RegisterRoutes(router.Group("/api/v1"), handler)
		body, _ := json.Marshal(ApprovalActionInput{Action: "PASS", Comment: "同意", Version: current.Version})
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/presale/requests/"+strconv.FormatUint(request.ID, 10)+"/approval-actions", strings.NewReader(string(body)))
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Idempotency-Key", step.key)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httpRequest)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("step %d status=%d body=%s", index+1, recorder.Code, recorder.Body.String())
		}
	}
	var final PresaleRequest
	if err = db.First(&final, request.ID).Error; err != nil {
		t.Fatal(err)
	}
	if final.Status != StatusApprovedPendingAssignment || final.CurrentApprovalNode != 0 || final.Version != 4 {
		t.Fatalf("final request status=%s node=%d version=%d", final.Status, final.CurrentApprovalNode, final.Version)
	}
	var logs int64
	if err = db.Model(&ApprovalLog{}).Where("tenant_id=? AND request_id=?", tenant, request.ID).Count(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if logs != 3 {
		t.Fatalf("approval logs=%d, want 3", logs)
	}
	var replays []MutationReplay
	if err = db.Where("tenant_id=? AND request_id=? ORDER BY id", tenant, request.ID).Find(&replays).Error; err != nil {
		t.Fatal(err)
	}
	if len(replays) != 3 || replays[0].Action != "NODE_1_PASS" || replays[1].Action != "NODE_2_PASS" || replays[2].Action != "NODE_3_PASS" {
		t.Fatalf("replays=%+v", replays)
	}
	var storedInstance ApprovalInstance
	if err = db.Where("tenant_id=? AND request_id=?", tenant, request.ID).First(&storedInstance).Error; err != nil {
		t.Fatal(err)
	}
	if storedInstance.Status != "APPROVED" || storedInstance.CurrentNode != 3 {
		t.Fatalf("instance status=%s node=%d", storedInstance.Status, storedInstance.CurrentNode)
	}
}
