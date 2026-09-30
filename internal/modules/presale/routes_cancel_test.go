package presale

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// 取消路由的能力门槛：presale.cancel 持有者与（无该权限码、只能撤销本人申请的）
// presale.create 持有者都必须能到达服务层，两者皆无的无关者在路由层即被 403。
// 申请者本人与状态约束仍由 Service.Cancel 在事务内收口。
func TestCancelRouteCapabilityGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name              string
		principal         auth.Principal
		actor             Actor
		requestStatus     RequestStatus
		wantStatus        int
		wantRequestUpdate bool
	}{
		{
			name:      "manager with presale.cancel passes",
			principal: auth.Principal{TenantID: "tenant-a", UserID: "lead", Permissions: map[string]struct{}{"presale.cancel": {}}},
			actor:     Actor{TenantID: "tenant-a", UserID: "lead", Permissions: map[string]bool{"presale.cancel": true}},
			// 审批通过后的申请只能由管理者或权限持有者取消。
			requestStatus: StatusApprovedPendingAssignment, wantStatus: http.StatusOK, wantRequestUpdate: true,
		},
		{
			name:      "applicant without cancel code passes gate",
			principal: auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Permissions: map[string]struct{}{"presale.create": {}}},
			actor:     Actor{TenantID: "tenant-a", UserID: "sales-a", Permissions: map[string]bool{"presale.create": true}},
			// 审批中的申请由申请者本人撤销，服务层不要求 presale.cancel。
			requestStatus: StatusPendingApproval, wantStatus: http.StatusOK, wantRequestUpdate: true,
		},
		{
			name:          "unrelated principal forbidden at route",
			principal:     auth.Principal{TenantID: "tenant-a", UserID: "tech", Permissions: map[string]struct{}{"presale.read": {}, "presale.progress": {}}},
			actor:         Actor{TenantID: "tenant-a", UserID: "tech", Permissions: map[string]bool{"presale.read": true, "presale.progress": true}},
			requestStatus: StatusPendingApproval, wantStatus: http.StatusForbidden,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &mutationRepository{
				request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, ApplicantID: "sales-a", Status: test.requestStatus},
				replays: map[string]*MutationReplay{},
			}
			service := NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)}, fixedIDs{})
			handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: test.actor})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), test.principal))
				c.Next()
			})
			RegisterRoutes(router.Group("/api/v1"), handler)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/presale/requests/9/cancel", strings.NewReader(`{"reason":"duplicate","version":3}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "cancel-gate-key")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantRequestUpdate && repo.requestUpdates != 1 {
				t.Fatalf("request updates=%d, want 1", repo.requestUpdates)
			}
		})
	}
}
