package contractreference

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
)

type actorContextRepository struct {
	*repositoryStub
	actor    auth.Principal
	actorErr error
}

// TestActorContextHTTPContract 验证正常HTTP响应保留恢复错误码并隐藏存储故障细节。
// 参数：t为测试执行器。
// 返回值：无；响应错误码、状态或敏感错误遮蔽不符时失败。
func TestActorContextHTTPContract(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"context", gorm.ErrRecordNotFound, 422, "CRM_CONTRACT_REFERENCE_ACTOR_CONTEXT_REQUIRED"},
		{"dependency", errors.New("private authorization storage failure"), 500, "COMMON_INTERNAL_ERROR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &actorContextRepository{repositoryStub: &repositoryStub{}, actorErr: tt.err}
			router := gin.New()
			router.GET("/customers/:customerID", NewHandler(NewService(r)).Resolve)
			request := httptest.NewRequest(http.MethodGet, "/customers/7", nil)
			request.Header.Set("X-Actor-Identity-ID", "actor-1")
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{TenantID: "tenant-a"}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var body struct{ Code, Message string }
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != tt.status || body.Code != tt.code || body.Message == tt.err.Error() {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

// ActorPrincipal 提供授权上下文边界的测试结果，不访问真实会话库。
// 参数：上下文、租户、主体和时间均由实际服务传入。
// 返回值：预设授权主体或查询错误。
func (r *actorContextRepository) ActorPrincipal(
	_ context.Context, _, _ string, _ time.Time,
) (auth.Principal, error) {
	return r.actor, r.actorErr
}

// TestResolveDistinguishesActorContext 验证过期上下文、权限不足和依赖失败不混淆。
// 参数：t为测试执行器。
// 返回值：无；错误分类或提前查询客户时测试失败。
func TestResolveDistinguishesActorContext(t *testing.T) {
	dependency := errors.New("authorization store unavailable")
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"missing-recent-session", gorm.ErrRecordNotFound,
			"CRM_CONTRACT_REFERENCE_ACTOR_CONTEXT_REQUIRED"},
		{"existing-session-without-read-permission", nil, "CRM_CONTRACT_REFERENCE_INVALID"},
		{"authorization-store-failure", dependency, "COMMON_INTERNAL_ERROR"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := &actorContextRepository{repositoryStub: &repositoryStub{}, actorErr: tt.err}
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a"})
			_, err := NewService(r).Resolve(ctx, 7, nil, "actor-1")
			actual := apperror.As(err)
			if actual.Code != tt.code || r.customerID != 0 {
				t.Fatalf("code=%s customerReads=%d want=%s", actual.Code, r.customerID, tt.code)
			}
			if tt.err == dependency && !errors.Is(err, dependency) {
				t.Fatal("依赖错误被吞掉")
			}
			if tt.err == gorm.ErrRecordNotFound && actual.HTTPStatus != http.StatusUnprocessableEntity {
				t.Fatal("新错误必须保留旧调用方的422兼容性")
			}
		})
	}
}
